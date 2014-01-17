# proxyswap

Hot swappable single host reverse TCP proxy in Go

_UNDER DEVELOPMENT_

## Features
* Swap server process with zero downtime
* Initiated simply via swap file touch
* Graceful shutdown
* Configurable while running

## Usage
<pre>
proxyswap -configPath PATH [OPTIONS]

PATH is a json config file as specified below.

Options:
  -listenPort Port on which to accept connections. Defaults to 8880.

Config keys:
  serverPath  Path to the server command. Required.
  serverArgs  Array of server arguments as strings. Defaults as empty.
  serverPort  Port where the server command is listening. Required.
  runningPath Path to use for the running server. Defaults to ./running.
  swapPath    Path to use for swap signaling. Defaults to ./swap.
  swapPoll    Seconds between checking the file mod time on the swap path. Defaults to 10.

Swapping:
First start proxyswap. While running make any config changes desired. Then touch the file at the swap path.
Basically this will:
 -pause accepting connections
 -wait for the current requests to finish
 -send an interrupt signal to the server
 -wait for the server to exit
 -rewrite the file at swap path
 -copy file at server path to running path
 -redirect server output to proxyswap's output
 -start server
 -wait for server to respond
 -continue accepting connections

Shutdown: An interrupt signal will gracefully shutdown the proxyswap without dropping connections.
</pre>

### Example: Swap while serving
Terminal 1:
```sh
go build proxyswap.go
go build exampleServer.go
./proxyswap -configPath exampleConfig.json
```

Terminal 2:
```sh
while [ 1 ]; do curl -s localhost:8880; done
```

Terminal 3:
```sh
while [ 1 ]; do touch swap && sleep 1; done
```
