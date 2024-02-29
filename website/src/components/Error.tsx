    import { Alert, AlertTitle, Box } from "@mui/material";

    
    export function Error() {
        return (           
            <Box display="flex" justifyContent="center" alignItems="center" minHeight="100vh">
                <Alert variant="filled" severity="error" sx={{m: 5, p: 3, minWidth: 500}}>
                    <AlertTitle>Error loading the page</AlertTitle>
                    An error has occurred!
                </Alert>
            </Box>           
        );
}
    
