
/**
 * SSHClient Class
 */
export class SSHClient {
    

    /**
    * Connect tries to connect to provided host and port
    * with provided username and password with ssh.
    * Returns state of connection and error. If error is not nil,
    * state will be false
    * @throws {Error} - if the operation fails
    */
    public Connect(host: string, port: number, username: string, password: string): boolean | null {
        return null;
    }
    

    /**
    * ConnectWithKey tries to connect to provided host and port
    * with provided username and private_key.
    * Returns state of connection and error. If error is not nil,
    * state will be false
    * @throws {Error} - if the operation fails
    */
    public ConnectWithKey(host: string, port: number, username: string, key: string): boolean | null {
        return null;
    }
    

    /**
    * ConnectSSHInfoMode tries to connect to provided host and port
    * with provided host and port
    * Returns HandshakeLog and error. If error is not nil,
    * state will be false
    * HandshakeLog is a struct that contains information about the
    * ssh connection
    * @throws {Error} - if the operation fails
    */
    public ConnectSSHInfoMode(host: string, port: number): any | null {
        return null;
    }
    

    /**
    * Run tries to open a new SSH session, then tries to execute
    * the provided command in said session
    * Returns string and error. If error is not nil,
    * state will be false
    * The string contains the command output
    * @throws {Error} - if the operation fails
    */
    public Run(cmd: string): string | null {
        return null;
    }
    

    /**
    * Close closes the SSH connection and destroys the client
    * Returns the success state and error. If error is not nil,
    * state will be false
    * @throws {Error} - if the operation fails
    */
    public Close(): boolean | null {
        return null;
    }
    

}

