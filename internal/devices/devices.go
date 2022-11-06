package devices

import (
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/freifunkMUC/wg-access-server/internal/network"
	"github.com/freifunkMUC/wg-access-server/internal/storage"
	"github.com/freifunkMUC/wg-access-server/pkg/authnz/authsession"

	"github.com/freifunkMUC/wg-embed/pkg/wgembed"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type DeviceManager struct {
	wg      wgembed.WireGuardInterface
	storage storage.Storage
	cidr    string
	cidrv6  string
}

func New(wg wgembed.WireGuardInterface, s storage.Storage, cidr, cidrv6 string) *DeviceManager {
	return &DeviceManager{wg, s, cidr, cidrv6}
}

func (d *DeviceManager) StartSync(disableMetadataCollection bool) error {
	// Start listening to the device add/remove events
	d.storage.OnAdd(func(device *storage.Device) {
		logrus.Debugf("storage event: device added: %s/%s", device.Owner, device.Name)
		if err := d.wg.AddPeer(device.PublicKey, network.SplitAddresses(device.Address)); err != nil {
			logrus.Error(errors.Wrap(err, "failed to add wireguard peer"))
		}
	})

	d.storage.OnDelete(func(device *storage.Device) {
		logrus.Debugf("storage event: device removed: %s/%s", device.Owner, device.Name)
		if err := d.wg.RemovePeer(device.PublicKey); err != nil {
			logrus.Error(errors.Wrap(err, "failed to remove wireguard peer"))
		}
	})

	d.storage.OnReconnect(func() {
		if err := d.sync(); err != nil {
			logrus.Error(errors.Wrap(err, "device sync after storage backend reconnect event failed"))
		}
	})

	// Do an initial sync of existing devices
	if err := d.sync(); err != nil {
		return errors.Wrap(err, "initial device sync from storage failed")
	}

	// start the metrics loop
	if !disableMetadataCollection {
		go metadataLoop(d)
	}

	return nil
}

func (d *DeviceManager) AddDevice(identity *authsession.Identity, name string, publicKey string) (*storage.Device, error) {
	if name == "" {
		return nil, errors.New("device name must not be empty")
	}

	clientAddr, err := d.nextClientAddress()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate an ip address for device")
	}

	device := &storage.Device{
		Owner:         identity.Subject,
		OwnerName:     identity.Name,
		OwnerEmail:    identity.Email,
		OwnerProvider: identity.Provider,
		Name:          name,
		PublicKey:     publicKey,
		Address:       clientAddr,
		CreatedAt:     time.Now(),
	}

	if err := d.SaveDevice(device); err != nil {
		return nil, errors.Wrap(err, "failed to save the new device")
	}

	return device, nil
}

func (d *DeviceManager) SaveDevice(device *storage.Device) error {
	return d.storage.Save(device)
}

func (d *DeviceManager) sync() error {
	devices, err := d.ListAllDevices()
	if err != nil {
		return errors.Wrap(err, "failed to list devices")
	}

	peers, err := d.wg.ListPeers()
	if err != nil {
		return errors.Wrap(err, "failed to list peers")
	}

	// Remove any peers for devices that are no longer in storage
	for _, peer := range peers {
		if !deviceListContains(devices, peer.PublicKey.String()) {
			if err := d.wg.RemovePeer(peer.PublicKey.String()); err != nil {
				logrus.Error(errors.Wrapf(err, "failed to remove peer during sync: %s", peer.PublicKey.String()))
			}
		}
	}

	// Add peers for all devices in storage
	for _, device := range devices {
		if err := d.wg.AddPeer(device.PublicKey, network.SplitAddresses(device.Address)); err != nil {
			logrus.Warn(errors.Wrapf(err, "failed to add device during sync: %s", device.Name))
		}
	}

	return nil
}

func (d *DeviceManager) ListAllDevices() ([]*storage.Device, error) {
	return d.storage.List("")
}

func (d *DeviceManager) ListDevices(user string) ([]*storage.Device, error) {
	return d.storage.List(user)
}

func (d *DeviceManager) DeleteDevice(user string, name string) error {
	device, err := d.storage.Get(user, name)
	if err != nil {
		return errors.Wrap(err, "failed to retrieve device")
	}

	if err := d.storage.Delete(device); err != nil {
		return err
	}

	return nil
}

func (d *DeviceManager) GetByPublicKey(publicKey string) (*storage.Device, error) {
	return d.storage.GetByPublicKey(publicKey)
}

var nextIPLock = sync.Mutex{}

func (d *DeviceManager) nextClientAddress() (string, error) {
	nextIPLock.Lock()
	defer nextIPLock.Unlock()

	devices, err := d.ListDevices("")
	if err != nil {
		return "", errors.Wrap(err, "failed to list devices")
	}

	// TODO: read up on better ways to allocate client's IP
	// addresses from a configurable CIDR

	usedIPv4s := make(map[netip.Addr]bool, len(devices)+3)
	usedIPv6s := make(map[netip.Addr]bool, len(devices)+3)

	// Check what IP addresses are already occupied
	for _, device := range devices {
		addresses := network.SplitAddresses(device.Address)
		for _, addr := range addresses {
			addr := netip.MustParsePrefix(addr).Addr()
			if addr.Is4() {
				usedIPv4s[addr] = true
			} else {
				usedIPv6s[addr] = true
			}
		}
	}

	var ipv4 string
	var ipv6 string

	if d.cidr != "" {
		vpnsubnetv4 := netip.MustParsePrefix(d.cidr)
		startIPv4 := vpnsubnetv4.Masked().Addr()

		// Add the network address and the VPN server address to the list of occupied addresses
		usedIPv4s[startIPv4] = true        // x.x.x.0
		usedIPv4s[startIPv4.Next()] = true // x.x.x.1

		for ip := startIPv4.Next().Next(); vpnsubnetv4.Contains(ip); ip = ip.Next() {
			if !usedIPv4s[ip] {
				ipv4 = netip.PrefixFrom(ip, 32).String()
				break
			}
		}
	}

	if d.cidrv6 != "" {
		vpnsubnetv6 := netip.MustParsePrefix(d.cidrv6)
		startIPv6 := vpnsubnetv6.Masked().Addr()

		// Add the network address and the VPN server address to the list of occupied addresses
		usedIPv6s[startIPv6] = true        // ::0
		usedIPv6s[startIPv6.Next()] = true // ::1

		for ip := startIPv6.Next().Next(); vpnsubnetv6.Contains(ip); ip = ip.Next() {
			if !usedIPv6s[ip] {
				ipv6 = netip.PrefixFrom(ip, 128).String()
				break
			}
		}
	}

	if ipv4 != "" {
		if ipv6 != "" {
			return fmt.Sprintf("%s, %s", ipv4, ipv6), nil
		} else if d.cidrv6 != "" {
			return "", fmt.Errorf("there are no free IP addresses in the vpn subnet: '%s'", d.cidrv6)
		} else {
			return ipv4, nil
		}
	} else if ipv6 != "" {
		if d.cidr != "" {
			return "", fmt.Errorf("there are no free IP addresses in the vpn subnet: '%s'", d.cidr)
		} else {
			return ipv6, nil
		}
	} else {
		return "", fmt.Errorf("there are no free IP addresses in the vpn subnets: '%s', '%s'", d.cidr, d.cidrv6)
	}
}

func deviceListContains(devices []*storage.Device, publicKey string) bool {
	for _, device := range devices {
		if device.PublicKey == publicKey {
			return true
		}
	}
	return false
}

func IsConnected(lastHandshake time.Time) bool {
	return lastHandshake.After(time.Now().Add(-3 * time.Minute))
}
