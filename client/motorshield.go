package client

import (
	"fmt"
	"sync"
	"time"

	"github.com/stianeikeland/go-rpio/v4"
)

// --- Software PWM ---------------------------------------------------------

// PWM implements a simple software PWM on a single pin.
type PWM struct {
	pin   rpio.Pin
	freq  time.Duration
	duty  float64 // 0–100
	quit  chan struct{}
	guard sync.Mutex
}

// NewPWM starts a 50 Hz PWM on the given pin.
func NewPWM(pin rpio.Pin, hz int) *PWM {
	p := &PWM{
		pin:  pin,
		freq: time.Second / time.Duration(hz),
		duty: 0,
		quit: make(chan struct{}),
	}
	pin.Output()
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
			p.pin.High()
			time.Sleep(high)
			p.pin.Low()
			time.Sleep(p.freq - high)
		case <-p.quit:
			p.pin.Low()
			return
		}
	}
}

// ChangeDutyCycle sets duty to 0–100.
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

// Stop halts the PWM goroutine and drives pin low.
func (p *PWM) Stop() {
	close(p.quit)
}

// --- Arrow (LED indicator) ------------------------------------------------

type Arrow struct {
	pin rpio.Pin
}

var arrowPins = map[int]rpio.Pin{
	1: rpio.Pin(13), // BOARD 33 → BCM13
	2: rpio.Pin(19), // BOARD 35 → BCM19
	3: rpio.Pin(26), // BOARD 37 → BCM26
	4: rpio.Pin(16), // BOARD 36 → BCM16
}

func NewArrow(which int) *Arrow {
	pin := arrowPins[which]
	pin.Output()
	pin.Low()
	return &Arrow{pin: pin}
}

func (a *Arrow) On()  { a.pin.High() }
func (a *Arrow) Off() { a.pin.Low() }

// --- Motor ----------------------------------------------------------------

type motorConfig struct {
	ePin, fPin, rPin rpio.Pin
	arrow            int
}

var motorConfigs = map[string]map[int]motorConfig{
	"MOTOR1": {
		1: {ePin: 17, fPin: 22, rPin: 27, arrow: 4}, // BOARD 11→BCM17,15→22,13→27
		2: {ePin: 17, fPin: 27, rPin: 22, arrow: 4},
	},
	"MOTOR2": {
		1: {ePin: 25, fPin: 23, rPin: 24, arrow: 25}, // BOARD 22→3,16→23,18→24
		2: {ePin: 25, fPin: 24, rPin: 23, arrow: 25},
	},
	"MOTOR3": {
		1: {ePin: 10, fPin: 9, rPin: 11, arrow: 2}, // BOARD 19→10,21→9,23→11
		2: {ePin: 10, fPin: 11, rPin: 9, arrow: 2},
	},
	"MOTOR4": {
		1: {ePin: 12, fPin: 8, rPin: 7, arrow: 1}, // BOARD 32→12,24→8,26→7
		2: {ePin: 12, fPin: 7, rPin: 8, arrow: 1},
	},
}

type Motor struct {
	pwm      *PWM
	fPin     rpio.Pin
	rPin     rpio.Pin
	arrow    *Arrow
	testMode bool
}

func NewMotor(name string, cfg int) *Motor {
	mc, ok := motorConfigs[name][cfg]
	if !ok {
		panic(fmt.Sprintf("invalid motor/config: %s/%d", name, cfg))
	}
	// Enable GPIO
	mc.ePin.Output()
	mc.fPin.Output()
	mc.rPin.Output()
	// Start off
	mc.ePin.Low()
	mc.fPin.Low()
	mc.rPin.Low()

	pwm := NewPWM(mc.ePin, 50)
	arrow := NewArrow(mc.arrow)

	return &Motor{
		pwm:   pwm,
		fPin:  mc.fPin,
		rPin:  mc.rPin,
		arrow: arrow,
	}
}

// Test mode: instead of driving motor, toggles the arrow LED.
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
	m.fPin.High()
	m.rPin.Low()
}

func (m *Motor) Reverse(speed float64) {
	fmt.Println("Reverse")
	if m.testMode {
		m.arrow.Off()
		return
	}
	m.pwm.ChangeDutyCycle(speed)
	m.fPin.Low()
	m.rPin.High()
}

func (m *Motor) Stop() {
	fmt.Println("Stop")
	m.arrow.Off()
	m.pwm.ChangeDutyCycle(0)
	m.fPin.Low()
	m.rPin.Low()
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
	en1, en2, c1, c2, c3, c4 rpio.Pin
}

var steppers = map[string]stepperPins{
	"STEPPER1": {en1: 17, en2: 3, c1: 27, c2: 22, c3: 24, c4: 23}, // adjust BOARD→BCM
	"STEPPER2": {en1: 10, en2: 12, c1: 9, c2: 11, c3: 8, c4: 7},
}

type Stepper struct {
	pins stepperPins
}

func NewStepper(name string) *Stepper {
	cfg, ok := steppers[name]
	if !ok {
		panic("invalid stepper: " + name)
	}
	for _, p := range []rpio.Pin{cfg.en1, cfg.en2, cfg.c1, cfg.c2, cfg.c3, cfg.c4} {
		p.Output()
		p.High()
	}
	// clear coils
	for _, p := range []rpio.Pin{cfg.c1, cfg.c2, cfg.c3, cfg.c4} {
		p.Low()
	}
	return &Stepper{pins: cfg}
}

func (s *Stepper) setStep(w1, w2, w3, w4 rpio.State) {
	s.pins.c1.Output()
	s.pins.c1.Write(w1)
	s.pins.c2.Output()
	s.pins.c2.Write(w2)
	s.pins.c3.Output()
	s.pins.c3.Write(w3)
	s.pins.c4.Output()
	s.pins.c4.Write(w4)
}

func (s *Stepper) Forward(delayMs time.Duration, steps int) {
	for i := 0; i < steps; i++ {
		s.setStep(rpio.High, rpio.Low, rpio.Low, rpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(rpio.Low, rpio.High, rpio.Low, rpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(rpio.Low, rpio.Low, rpio.High, rpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(rpio.Low, rpio.Low, rpio.Low, rpio.High)
		time.Sleep(delayMs * time.Millisecond)
	}
}

func (s *Stepper) Backward(delayMs time.Duration, steps int) {
	for i := 0; i < steps; i++ {
		s.setStep(rpio.Low, rpio.Low, rpio.Low, rpio.High)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(rpio.Low, rpio.Low, rpio.High, rpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(rpio.Low, rpio.High, rpio.Low, rpio.Low)
		time.Sleep(delayMs * time.Millisecond)
		s.setStep(rpio.High, rpio.Low, rpio.Low, rpio.Low)
		time.Sleep(delayMs * time.Millisecond)
	}
}

func (s *Stepper) Stop() {
	fmt.Println("Stop Stepper Motor")
	for _, p := range []rpio.Pin{s.pins.c1, s.pins.c2, s.pins.c3, s.pins.c4} {
		p.Low()
	}
}

// --- Sensor ---------------------------------------------------------------

type Sensor struct {
	echo      rpio.Pin
	trigger   *rpio.Pin // nil if not used
	boundary  float64
	Triggered bool
	lastRead  float64
	check     func(*Sensor)
}

func NewSensor(sensortype string, boundary float64) *Sensor {
	var s Sensor
	s.boundary = boundary

	switch sensortype {
	case "IR1", "IR2":
		// BOARD 7→BCM4, BOARD12→BCM18
		if sensortype == "IR1" {
			s.echo = rpio.Pin(4)
		} else {
			s.echo = rpio.Pin(18)
		}
		s.check = func(s *Sensor) {
			if s.echo.Read() == rpio.High {
				fmt.Println("Sensor:", sensortype, "Object Detected")
				s.Triggered = true
			} else {
				s.Triggered = false
			}
		}

	case "ULTRASONIC":
		// BOARD29→BCM5, BOARD31→BCM6
		t := rpio.Pin(5)
		t.Output()
		e := rpio.Pin(6)
		e.Input()
		s.trigger = &t
		s.echo = e
		s.check = func(s *Sensor) {
			s.trigger.Write(rpio.High)
			time.Sleep(10 * time.Microsecond)
			s.trigger.Write(rpio.Low)

			start := time.Now()
			for s.echo.Read() == rpio.Low {
				start = time.Now()
			}
			for s.echo.Read() == rpio.High {
			}
			elapsed := time.Since(start)
			dist := elapsed.Seconds() * 34300.0 / 2
			s.lastRead = dist
			if dist < s.boundary {
				fmt.Println("Boundary breached:", dist)
				s.Triggered = true
			} else {
				s.Triggered = false
			}
		}

	default:
		panic("unknown sensor: " + sensortype)
	}

	// ensure echo pin is input
	s.echo.Input()
	return &s
}

func (s *Sensor) Trigger() {
	s.check(s)
	fmt.Println("Trigger Called; Triggered =", s.Triggered)
}

