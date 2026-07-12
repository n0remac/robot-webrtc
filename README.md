# robot-webrtc

Local-network control stack for a Raspberry Pi robot. Despite the historical
repository name, the robot client now uses plain HTTP, MJPEG, and WebSockets;
WebRTC is no longer required for normal operation.

## Architecture

The browser connects directly to the robot:

```text
Browser -- HTTP/WebSocket --> cmd/client -- GPIO --> motors
   |                            |
   |                            +-- in-process servo service --> PCA9685
   |
   +-- GET /stream --> cmd/client proxy --> uStreamer --> /dev/video0
```

`cmd/client` initializes the motors and servos, starts and supervises uStreamer,
serves the controller page, accepts press/release commands at `/ws/control`, and
proxies the camera at `/stream`. A heartbeat timeout and every WebSocket
disconnect stop all motors and servos.

## Robot setup

The robot needs Go, uStreamer, GPIO/I2C access, `/dev/video0`, and the generated
servo protobuf code already in this repository. Install uStreamer using the
package or build instructions for the robot's Linux distribution.

Start the robot with one command:

```sh
go run ./cmd/client
```

The Go process starts uStreamer itself, binds it to loopback, and stops it when
the robot process shuts down. uStreamer must be installed, but it does not need
to be started separately. The standalone `cmd/servo` command remains available
for servo-only development and hardware testing.

Then open `http://ROBOT_IP:8080/` from a device on the same network. If mDNS is
configured on the robot, `http://robot.local:8080/` can be used instead.

Useful client flags:

```text
-addr    HTTP listen address (default :8080)
-video-binary      uStreamer executable (default ustreamer)
-video-device      camera device (default /dev/video0)
-video-resolution  camera resolution (default 640x480)
-video-fps         camera frame rate (default 30)
-video-port        internal loopback port (default 8081)
```

uStreamer is bound to `127.0.0.1` intentionally: browsers reach it through the
Go server's `/stream` proxy, so the camera does not expose a second network port.

## Controls

- Drive: `W`, `A`, `S`, `D`
- Arm/claw: `T`, `F`, `G`, `H`, `R`, `Y`
- Camera pan/tilt: `I`, `J`, `K`, `L`

Keyboard and pointer/touch controls are supported. Opening the page in a second
browser replaces the first active controller and issues a safety stop.

## Verification

Run all automated tests with:

```sh
go test ./...
```
