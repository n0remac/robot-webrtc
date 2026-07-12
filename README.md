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
   +-- GET /stream --> Go MJPEG handler <-- FFmpeg <-- /dev/video0
```

`cmd/client` initializes the motors and servos, starts and supervises FFmpeg,
serves the controller page, accepts press/release commands at `/ws/control`, and
serves FFmpeg's JPEG frames at `/stream`. A heartbeat timeout and every
WebSocket disconnect stop all motors and servos.

## Robot setup

The robot needs Go, FFmpeg, GPIO/I2C access, `/dev/video0`, and the generated
servo protobuf code already in this repository.

Start the robot with one command:

```sh
go run ./cmd/client
```

The Go process starts FFmpeg itself, reads MJPEG frames from its stdout, and
stops it when the robot process shuts down. FFmpeg must be installed, but it
does not need to be started separately. The standalone `cmd/servo` command
remains available for servo-only development and hardware testing.

Then open `http://ROBOT_IP:8080/` from a device on the same network. If mDNS is
configured on the robot, `http://robot.local:8080/` can be used instead.

Useful client flags:

```text
-addr    HTTP listen address (default :8080)
-video-binary      FFmpeg executable (default ffmpeg)
-video-device      camera device (default /dev/video0)
-video-resolution  camera resolution (default 640x480)
-video-fps         camera frame rate (default 30)
```

FFmpeg does not open a network port. Only the Go control server is exposed.

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
