package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
	_ "periph.io/x/host/v3/bcm283x" // enable the BCM283x MMIO driver
	"periph.io/x/host/v3/rpi"       // header-pin definitions
	_ "periph.io/x/host/v3/sysfs"   // sysfs fallback GPIO driver
)

func init() {
	if _, err := host.Init(); err != nil {
		log.Fatalf("periph host.Init: %v", err)
	}
}

// --- Software PWM ---------------------------------------------------------

// PWM implements a simple software PWM on a single GPIO pin.
type PWM struct {
	pin   gpio.PinIO
	freq  time.Duration
	duty  float64 // 0–100
	quit  chan struct{}
	guard sync.Mutex
}

// NewPWM starts a software PWM at the given frequency (hz) on the named GPIO.
func NewPWM(pinName string, hz int) *PWM {
	pin := gpioreg.ByName(pinName)
	if pin == nil {
		log.Fatalf("failed to find GPIO pin %q", pinName)
	}
	if err := pin.Out(gpio.Low); err != nil {
		log.Fatalf("failed to set %s low: %v", pinName, err)
	}
	p := &PWM{
		pin:  pin,
		freq: time.Second / time.Duration(hz),
		quit: make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *PWM) run() {
	ticker := time.NewTicker(p.freq)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.guard.Lock()
			d := p.duty / 100.0
			p.guard.Unlock()
			high := time.Duration(float64(p.freq) * d)
			_ = p.pin.Out(gpio.High)
			time.Sleep(high)
			_ = p.pin.Out(gpio.Low)
			time.Sleep(p.freq - high)
		case <-p.quit:
			_ = p.pin.Out(gpio.Low)
			return
		}
	}
}

// ChangeDutyCycle sets the PWM duty cycle (0–100%).
func (p *PWM) ChangeDutyCycle(duty float64) {
	if duty < 0 {
		duty = 0
	} else if duty > 100 {
		duty = 100
	}
	p.guard.Lock()
	p.duty = duty
	p.guard.Unlock()
}

// Stop halts the PWM goroutine and drives the pin low.
func (p *PWM) Stop() {
	close(p.quit)
}

// --- Arrow (LED indicator) ------------------------------------------------

type Arrow struct {
	pin gpio.PinIO
}

var arrowPins = map[int]string{
	1: "GPIO13", // BOARD 33 → BCM13
	2: "GPIO19", // BOARD 35 → BCM19
	3: "GPIO26", // BOARD 37 → BCM26
	4: "GPIO16", // BOARD 36 → BCM16
}

// NewArrow returns an Arrow tied to the specified indicator number.
func NewArrow(which int) *Arrow {
	name, ok := arrowPins[which]
	if !ok {
		log.Fatalf("invalid arrow index: %d", which)
	}
	p := gpioreg.ByName(name)
	if p == nil {
		log.Fatalf("failed to find GPIO pin %q", name)
	}
	if err := p.Out(gpio.Low); err != nil {
		log.Fatalf("failed to set arrow %d low: %v", which, err)
	}
	return &Arrow{pin: p}
}

func (a *Arrow) On()  { _ = a.pin.Out(gpio.High) }
func (a *Arrow) Off() { _ = a.pin.Out(gpio.Low) }

// --- Motor ----------------------------------------------------------------

type motorConfig struct {
	ePin    gpio.PinIO
	fPin    gpio.PinIO
	rPin    gpio.PinIO
	arrowID int
}

var motorConfigs = map[string]map[int]motorConfig{
	"MOTOR1": {
		1: {ePin: rpi.P1_11, fPin: rpi.P1_15, rPin: rpi.P1_13, arrowID: 4},
		2: {ePin: rpi.P1_11, fPin: rpi.P1_13, rPin: rpi.P1_15, arrowID: 4},
	},
	"MOTOR2": {
		1: {
			ePin:    rpi.P1_22, // BCM25
			fPin:    rpi.P1_16, // BCM23
			rPin:    rpi.P1_18, // BCM24
			arrowID: 2,
		},
		2: {
			ePin:    rpi.P1_22, // BCM25
			fPin:    rpi.P1_18, // BCM24
			rPin:    rpi.P1_16, // BCM23
			arrowID: 2,
		},
	},
	"MOTOR3": {
		1: {
			ePin:    rpi.P1_19, // BCM10
			fPin:    rpi.P1_21, // BCM9
			rPin:    rpi.P1_23, // BCM11
			arrowID: 2,
		},
		2: {
			ePin:    rpi.P1_19, // BCM10
			fPin:    rpi.P1_23, // BCM11
			rPin:    rpi.P1_21, // BCM9
			arrowID: 2,
		},
	},
	"MOTOR4": {
		1: {
			ePin:    rpi.P1_32, // BCM12
			fPin:    rpi.P1_24, // BCM8
			rPin:    rpi.P1_26, // BCM7
			arrowID: 1,
		},
		2: {
			ePin:    rpi.P1_32, // BCM12
			fPin:    rpi.P1_26, // BCM7
			rPin:    rpi.P1_24, // BCM8
			arrowID: 1,
		},
	},
}

type Motor struct {
	pwm      *PWM
	fPin     gpio.PinIO
	rPin     gpio.PinIO
	arrow    *Arrow
	testMode bool
}

// NewMotor initializes a Motor by name ("MOTOR1"–"MOTOR4") and config index.
func NewMotor(name string, cfg int) *Motor {
	mcMap, ok := motorConfigs[name]
	if !ok {
		log.Fatalf("invalid motor name: %s", name)
	}
	mc, ok := mcMap[cfg]
	if !ok {
		log.Fatalf("invalid motor config %d for %s", cfg, name)
	}
	for _, p := range []gpio.PinIO{mc.ePin, mc.fPin, mc.rPin} {
		if err := p.Out(gpio.Low); err != nil {
			log.Fatalf("failed to set %v low: %v", p, err)
		}
	}
	pwm := NewPWM(mc.ePin.Name(), 50)
	arrow := NewArrow(mc.arrowID)
	return &Motor{pwm: pwm, fPin: mc.fPin, rPin: mc.rPin, arrow: arrow}
}

// Test mode: toggles the arrow LED instead of driving the motor.
func (m *Motor) Test(state bool) {
	m.testMode = state
}

func (m *Motor) Forward(speed float64) {
	fmt.Println("Forward")
	if m.testMode {
		m.arrow.On()
		return
	}
	m.pwm.ChangeDutyCycle(speed)
	_ = m.fPin.Out(gpio.High)
	_ = m.rPin.Out(gpio.Low)
}

func (m *Motor) Reverse(speed float64) {
	fmt.Println("Reverse")
	if m.testMode {
		m.arrow.Off()
		return
	}
	m.pwm.ChangeDutyCycle(speed)
	_ = m.fPin.Out(gpio.Low)
	_ = m.rPin.Out(gpio.High)
}

func (m *Motor) Stop() {
	fmt.Println("Stop")
	m.arrow.Off()
	m.pwm.ChangeDutyCycle(0)
	_ = m.fPin.Out(gpio.Low)
	_ = m.rPin.Out(gpio.Low)
}

// --- Linked Motors --------------------------------------------------------

type LinkedMotors struct {
	motors []*Motor
}

func NewLinkedMotors(ms ...*Motor) *LinkedMotors {
	return &LinkedMotors{motors: ms}
}

func (lm *LinkedMotors) Forward(speed float64) {
	for _, m := range lm.motors {
		m.Forward(speed)
	}
}

func (lm *LinkedMotors) Reverse(speed float64) {
	for _, m := range lm.motors {
		m.Reverse(speed)
	}
}

func (lm *LinkedMotors) Stop() {
	for _, m := range lm.motors {
		m.Stop()
	}
}

// --- Stepper --------------------------------------------------------------

type stepperPins struct {
	en1, en2, c1, c2, c3, c4 gpio.PinIO
}

var stepperConfigs = map[string]stepperPins{
	"STEPPER1": {
		en1: gpioreg.ByName("GPIO17"),
		en2: gpioreg.ByName("GPIO03"),
		c1:  gpioreg.ByName("GPIO27"),
		c2:  gpioreg.ByName("GPIO22"),
		c3:  gpioreg.ByName("GPIO24"),
		c4:  gpioreg.ByName("GPIO23"),
	},
	"STEPPER2": {
		en1: gpioreg.ByName("GPIO10"),
		en2: gpioreg.ByName("GPIO12"),
		c1:  gpioreg.ByName("GPIO09"),
		c2:  gpioreg.ByName("GPIO11"),
		c3:  gpioreg.ByName("GPIO08"),
		c4:  gpioreg.ByName("GPIO07"),
	},
}

type Stepper struct {
	pins stepperPins
}

// NewStepper initializes a stepper by name ("STEPPER1" or "STEPPER2").
func NewStepper(name string) *Stepper {
	cfg, ok := stepperConfigs[name]
	if !ok {
		log.Fatalf("invalid stepper name: %s", name)
	}
	// Enable and clear coils
	for _, p := range []gpio.PinIO{cfg.en1, cfg.en2, cfg.c1, cfg.c2, cfg.c3, cfg.c4} {
		if p == nil {
			log.Fatalf("failed to find GPIO for stepper %s", name)
		}
		if err := p.Out(gpio.High); err != nil {
			log.Fatalf("failed to set pin %v high: %v", p, err)
		}
	}
	for _, coil := range []gpio.PinIO{cfg.c1, cfg.c2, cfg.c3, cfg.c4} {
		_ = coil.Out(gpio.Low)
	}
	return &Stepper{pins: cfg}
}

func (s *Stepper) setStep(w1, w2, w3, w4 gpio.Level) {
	_ = s.pins.c1.Out(w1)
	_ = s.pins.c2.Out(w2)
	_ = s.pins.c3.Out(w3)
	_ = s.pins.c4.Out(w4)
}

// Forward steps the motor forward with given step delay (in ms) and count.
func (s *Stepper) Forward(delayMs time.Duration, steps int) {
	for i := 0; i < steps; i++ {
		s.setStep(gpio.High, gpio.Low, gpio.Low, gpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(gpio.Low, gpio.High, gpio.Low, gpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(gpio.Low, gpio.Low, gpio.High, gpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(gpio.Low, gpio.Low, gpio.Low, gpio.High)
		time.Sleep(delayMs * time.Millisecond)
	}
}

// Backward reverses the stepping sequence.
func (s *Stepper) Backward(delayMs time.Duration, steps int) {
	for i := 0; i < steps; i++ {
		s.setStep(gpio.Low, gpio.Low, gpio.Low, gpio.High)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(gpio.Low, gpio.Low, gpio.High, gpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(gpio.Low, gpio.High, gpio.Low, gpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(gpio.High, gpio.Low, gpio.Low, gpio.Low)
		time.Sleep(delayMs * time.Millisecond)
	}
}

// Stop de-energizes all coils.
func (s *Stepper) Stop() {
	fmt.Println("Stop Stepper Motor")
	for _, coil := range []gpio.PinIO{s.pins.c1, s.pins.c2, s.pins.c3, s.pins.c4} {
		_ = coil.Out(gpio.Low)
	}
}

// --- Sensor ---------------------------------------------------------------

type Sensor struct {
	echo       gpio.PinIO
	trigger    gpio.PinIO // optional, for ultrasonic
	useTrigger bool
	boundary   float64
	Triggered  bool
	lastRead   float64
	check      func(*Sensor)
}

// NewSensor creates an IR or Ultrasonic sensor by type ("IR1", "IR2", "ULTRASONIC").
func NewSensor(sensortype string, boundary float64) *Sensor {
	var s Sensor
	s.boundary = boundary

	switch sensortype {
	case "IR1", "IR2":
		pinName := map[string]string{"IR1": "GPIO04", "IR2": "GPIO18"}[sensortype]
		p := gpioreg.ByName(pinName)
		if p == nil {
			log.Fatalf("failed to find GPIO pin %q for %s", pinName, sensortype)
		}
		if err := p.In(gpio.PullNoChange, gpio.NoEdge); err != nil {
			log.Fatalf("failed to set %s as input: %v", pinName, err)
		}
		s.echo = p
		s.check = func(s *Sensor) {
			s.Triggered = s.echo.Read() == gpio.High
			if s.Triggered {
				fmt.Println("Sensor:", sensortype, "Object Detected")
			}
		}

	case "ULTRASONIC":
		tName, eName := "GPIO05", "GPIO06"
		t := gpioreg.ByName(tName)
		e := gpioreg.ByName(eName)
		if t == nil || e == nil {
			log.Fatalf("failed to find GPIO pins for ultrasonic: %s, %s", tName, eName)
		}
		if err := t.Out(gpio.Low); err != nil {
			log.Fatalf("failed to set trigger low: %v", err)
		}
		if err := e.In(gpio.PullNoChange, gpio.NoEdge); err != nil {
			log.Fatalf("failed to set echo input: %v", err)
		}
		s.trigger = t
		s.echo = e
		s.useTrigger = true
		s.check = func(s *Sensor) {
			_ = s.trigger.Out(gpio.High)
			time.Sleep(10 * time.Microsecond)
			_ = s.trigger.Out(gpio.Low)

			start := time.Now()
			for s.echo.Read() == gpio.Low {
				start = time.Now()
			}
			for s.echo.Read() == gpio.High {
			}
			elapsed := time.Since(start)
			dist := elapsed.Seconds() * 34300.0 / 2
			s.lastRead = dist
			s.Triggered = dist < s.boundary
			if s.Triggered {
				fmt.Printf("Boundary breached: %.2f mm\n", dist)
			}
		}

	default:
		log.Fatalf("unknown sensor type: %s", sensortype)
	}

	return &s
}

// Trigger reads the sensor and updates s.Triggered.
func (s *Sensor) Trigger() {
	s.check(s)
	fmt.Println("Trigger Called; Triggered =", s.Triggered)
}
