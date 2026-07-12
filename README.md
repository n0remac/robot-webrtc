# robot-webrtc

Local-network control stack for a Raspberry Pi robot. Despite the historical
repository name, the robot client now uses plain HTTP, MJPEG, and WebSockets;
WebRTC is no longer required for normal operation.

## Architecture

The browser connects directly to the robot:

```text
Browser -- HTTP/WebSocket --> cmd/client -- GPIO --> motors
   |                            |
   |                            +-- gRPC --> cmd/servo --> PCA9685
   |
   +-- GET /stream --> cmd/client proxy --> uStreamer --> /dev/video0
```

`cmd/client` serves the controller page, accepts press/release commands at
`/ws/control`, and proxies the local uStreamer camera at `/stream`. A heartbeat
timeout and every WebSocket disconnect stop all motors and servos.

The previous Pion client remains available only as legacy source behind the
`webrtc_legacy` build tag in `client/client.go`. It is not part of the default
robot build.

## Robot setup

The robot needs Go, uStreamer, GPIO/I2C access, `/dev/video0`, and the generated
servo protobuf code already in this repository. Install uStreamer using the
package or build instructions for the robot's Linux distribution.

Start the three processes on the robot:

```sh
go run ./cmd/servo

ustreamer \
  --device=/dev/video0 \
  --resolution=640x480 \
  --desired-fps=30 \
  --host=127.0.0.1 \
  --port=8081

go run ./cmd/client
```

Then open `http://ROBOT_IP:8080/` from a device on the same network. If mDNS is
configured on the robot, `http://robot.local:8080/` can be used instead.

Useful client flags:

```text
-addr    HTTP listen address (default :8080)
-camera  uStreamer base URL (default http://127.0.0.1:8081)
-servo   servo gRPC address (default 127.0.0.1:50051)
```

Binding uStreamer to `127.0.0.1` is intentional: browsers reach it through the
Go server's `/stream` proxy, so the camera does not need a second exposed port.

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
