import CircularProgress from '@mui/material/CircularProgress';
import Box from '@mui/material/Box';

interface LoadingProps {
    message: String;
}

export function Loading({ message }: LoadingProps) {

    return (
        <Box component="div" m={4} display="flex" flexDirection="column" justifyContent="center" alignItems="center" minHeight="50vh" >
            <Box mb={5}>Loading { message } ...</Box>
            <CircularProgress color="primary" />
        </Box>
    );
  }
  