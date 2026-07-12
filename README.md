# robot-webrtc

Raspberry Pi robot client for the Orcas Makers website. Despite the historical
repository name, the robot now uses an outbound WebSocket for controls and
JPEG camera frames; WebRTC is no longer required for normal operation.

## Architecture

The robot connects outward to the website, so the Pi does not host a public UI:

```text
Browser <-- HTTP/WebSocket --> OrcasMakers website <-- WebSocket -- cmd/client -- GPIO
                                      ^                                |
                                      +------ JPEG camera frames ------+
```

`cmd/client` initializes the motors and servos, starts and supervises FFmpeg,
then maintains an outbound connection to `/ws/robot` on the website. Every
website disconnect or controller timeout stops all motors and servos.

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

For local development the client connects to `http://localhost:8081`. Override
that with `ROBOT_SITE_URL` or `-site-url`; set `ROBOT_TOKEN` to the same value
used by the website. Production ARM builds can inject `ROBOT_SITE_URL` from a
GitHub secret with `-ldflags "-X main.defaultSiteURL=$ROBOT_SITE_URL"`.

Useful client flags:

```text
-site-url          Orcas Makers website (default http://localhost:8081)
-robot-token       robot authentication token (default ROBOT_TOKEN)
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
