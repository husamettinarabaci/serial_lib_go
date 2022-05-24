package devhsmtekserial

import (
	"errors"
	"io"
	"math"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

//
// Grab the constants with the following little program, to avoid using cgo:
//
// #include <stdio.h>
// #include <stdlib.h>
// #include <linux/termios.h>
//
// int main(int argc, const char **argv) {
//   printf("TCSETS2 = 0x%08X\n", TCSETS2);
//   printf("BOTHER  = 0x%08X\n", BOTHER);
//   printf("NCCS    = %d\n",     NCCS);
//   return 0;
// }
//
const (
	kTCSETS2 = 0x402C542B
	kBOTHER  = 0x1000
	kNCCS    = 19
)

//
// Types from asm-generic/termbits.h
//

type cc_t byte
type speed_t uint32
type tcflag_t uint32
type termios2 struct {
	c_iflag  tcflag_t    // input mode flags
	c_oflag  tcflag_t    // output mode flags
	c_cflag  tcflag_t    // control mode flags
	c_lflag  tcflag_t    // local mode flags
	c_line   cc_t        // line discipline
	c_cc     [kNCCS]cc_t // control characters
	c_ispeed speed_t     // input speed
	c_ospeed speed_t     // output speed
}

// Constants for RS485 operation

const (
	sER_RS485_ENABLED        = (1 << 0)
	sER_RS485_RTS_ON_SEND    = (1 << 1)
	sER_RS485_RTS_AFTER_SEND = (1 << 2)
	sER_RS485_RX_DURING_TX   = (1 << 4)
	tIOCSRS485               = 0x542F
)

type serial_rs485 struct {
	flags                 uint32
	delay_rts_before_send uint32
	delay_rts_after_send  uint32
	padding               [5]uint32
}

//
// Returns a pointer to an instantiates termios2 struct, based on the given
// OpenOptions. Termios2 is a Linux extension which allows arbitrary baud rates
// to be specified.
//
func makeTermios2(options OpenOptions) (*termios2, error) {

	// Sanity check inter-character timeout and minimum read size options.

	vtime := uint(round(float64(options.InterCharacterTimeout)/100.0) * 100)
	vmin := options.MinimumReadSize

	if vmin == 0 && vtime < 100 {
		return nil, errors.New("invalid values for InterCharacterTimeout and MinimumReadSize")
	}

	if vtime > 25500 {
		return nil, errors.New("invalid value for InterCharacterTimeout")
	}

	ccOpts := [kNCCS]cc_t{}
	ccOpts[syscall.VTIME] = cc_t(vtime / 100)
	ccOpts[syscall.VMIN] = cc_t(vmin)

	t2 := &termios2{
		c_cflag:  syscall.CLOCAL | syscall.CREAD | kBOTHER,
		c_ispeed: speed_t(options.BaudRate),
		c_ospeed: speed_t(options.BaudRate),
		c_cc:     ccOpts,
	}

	switch options.StopBits {
	case 1:
	case 2:
		t2.c_cflag |= syscall.CSTOPB

	default:
		return nil, errors.New("invalid setting for StopBits")
	}

	switch options.ParityMode {
	case PARITY_NONE:
	case PARITY_ODD:
		t2.c_cflag |= syscall.PARENB
		t2.c_cflag |= syscall.PARODD

	case PARITY_EVEN:
		t2.c_cflag |= syscall.PARENB

	default:
		return nil, errors.New("invalid setting for ParityMode")
	}

	switch options.DataBits {
	case 5:
		t2.c_cflag |= syscall.CS5
	case 6:
		t2.c_cflag |= syscall.CS6
	case 7:
		t2.c_cflag |= syscall.CS7
	case 8:
		t2.c_cflag |= syscall.CS8
	default:
		return nil, errors.New("invalid setting for DataBits")
	}

	if options.RTSCTSFlowControl {
		t2.c_cflag |= unix.CRTSCTS
	}

	return t2, nil
}

func openInternal(options OpenOptions) (io.ReadWriteCloser, error) {

	file, openErr :=
		os.OpenFile(
			options.PortName,
			syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK,
			0600)
	if openErr != nil {
		return nil, openErr
	}

	// Clear the non-blocking flag set above.
	nonblockErr := syscall.SetNonblock(int(file.Fd()), false)
	if nonblockErr != nil {
		return nil, nonblockErr
	}

	t2, optErr := makeTermios2(options)
	if optErr != nil {
		return nil, optErr
	}

	r, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(file.Fd()),
		uintptr(kTCSETS2),
		uintptr(unsafe.Pointer(t2)))

	if errno != 0 {
		return nil, os.NewSyscallError("SYS_IOCTL", errno)
	}

	if r != 0 {
		return nil, errors.New("unknown error from SYS_IOCTL")
	}

	if options.Rs485Enable {
		rs485 := serial_rs485{
			sER_RS485_ENABLED,
			uint32(options.Rs485DelayRtsBeforeSend),
			uint32(options.Rs485DelayRtsAfterSend),
			[5]uint32{0, 0, 0, 0, 0},
		}

		if options.Rs485RtsHighDuringSend {
			rs485.flags |= sER_RS485_RTS_ON_SEND
		}

		if options.Rs485RtsHighAfterSend {
			rs485.flags |= sER_RS485_RTS_AFTER_SEND
		}

		r, _, errno := syscall.Syscall(
			syscall.SYS_IOCTL,
			uintptr(file.Fd()),
			uintptr(tIOCSRS485),
			uintptr(unsafe.Pointer(&rs485)))

		if errno != 0 {
			return nil, os.NewSyscallError("SYS_IOCTL (RS485)", errno)
		}

		if r != 0 {
			return nil, errors.New("Unknown error from SYS_IOCTL (RS485)")
		}
	}

	return file, nil
}

// Valid parity values.
type ParityMode int

const (
	PARITY_NONE ParityMode = 0
	PARITY_ODD  ParityMode = 1
	PARITY_EVEN ParityMode = 2
)

var (
	// The list of standard baud-rates.
	StandardBaudRates = map[uint]bool{
		50:     true,
		75:     true,
		110:    true,
		134:    true,
		150:    true,
		200:    true,
		300:    true,
		600:    true,
		1200:   true,
		1800:   true,
		2400:   true,
		4800:   true,
		7200:   true,
		9600:   true,
		14400:  true,
		19200:  true,
		28800:  true,
		38400:  true,
		57600:  true,
		76800:  true,
		115200: true,
		230400: true,
	}
)

// IsStandardBaudRate checks whether the specified baud-rate is standard.
//
// Some operating systems may support non-standard baud-rates (OSX) via
// additional IOCTL.
func IsStandardBaudRate(baudRate uint) bool { return StandardBaudRates[baudRate] }

// OpenOptions is the struct containing all of the options necessary for
// opening a serial port.
type OpenOptions struct {
	// The name of the port, e.g. "/dev/tty.usbserial-A8008HlV".
	PortName string

	// The baud rate for the port.
	BaudRate uint

	// The number of data bits per frame. Legal values are 5, 6, 7, and 8.
	DataBits uint

	// The number of stop bits per frame. Legal values are 1 and 2.
	StopBits uint

	// The type of parity bits to use for the connection. Currently parity errors
	// are simply ignored; that is, bytes are delivered to the user no matter
	// whether they were received with a parity error or not.
	ParityMode ParityMode

	// Enable RTS/CTS (hardware) flow control.
	RTSCTSFlowControl bool

	// An inter-character timeout value, in milliseconds, and a minimum number of
	// bytes to block for on each read. A call to Read() that otherwise may block
	// waiting for more data will return immediately if the specified amount of
	// time elapses between successive bytes received from the device or if the
	// minimum number of bytes has been exceeded.
	//
	// Note that the inter-character timeout value may be rounded to the nearest
	// 100 ms on some systems, and that behavior is undefined if calls to Read
	// supply a buffer whose length is less than the minimum read size.
	//
	// Behaviors for various settings for these values are described below. For
	// more information, see the discussion of VMIN and VTIME here:
	//
	//     http://www.unixwiz.net/techtips/termios-vmin-vtime.html
	//
	// InterCharacterTimeout = 0 and MinimumReadSize = 0 (the default):
	//     This arrangement is not legal; you must explicitly set at least one of
	//     these fields to a positive number. (If MinimumReadSize is zero then
	//     InterCharacterTimeout must be at least 100.)
	//
	// InterCharacterTimeout > 0 and MinimumReadSize = 0
	//     If data is already available on the read queue, it is transferred to
	//     the caller's buffer and the Read() call returns immediately.
	//     Otherwise, the call blocks until some data arrives or the
	//     InterCharacterTimeout milliseconds elapse from the start of the call.
	//     Note that in this configuration, InterCharacterTimeout must be at
	//     least 100 ms.
	//
	// InterCharacterTimeout > 0 and MinimumReadSize > 0
	//     Calls to Read() return when at least MinimumReadSize bytes are
	//     available or when InterCharacterTimeout milliseconds elapse between
	//     received bytes. The inter-character timer is not started until the
	//     first byte arrives.
	//
	// InterCharacterTimeout = 0 and MinimumReadSize > 0
	//     Calls to Read() return only when at least MinimumReadSize bytes are
	//     available. The inter-character timer is not used.
	//
	// For windows usage, these options (termios) do not conform well to the
	//     windows serial port / comms abstractions.  Please see the code in
	//		 open_windows setCommTimeouts function for full documentation.
	//   	 Summary:
	//			Setting MinimumReadSize > 0 will cause the serialPort to block until
	//			until data is available on the port.
	//			Setting IntercharacterTimeout > 0 and MinimumReadSize == 0 will cause
	//			the port to either wait until IntercharacterTimeout wait time is
	//			exceeded OR there is character data to return from the port.
	//

	InterCharacterTimeout uint
	MinimumReadSize       uint

	// Use to enable RS485 mode -- probably only valid on some Linux platforms
	Rs485Enable bool

	// Set to true for logic level high during send
	Rs485RtsHighDuringSend bool

	// Set to true for logic level high after send
	Rs485RtsHighAfterSend bool

	// set to receive data during sending
	Rs485RxDuringTx bool

	// RTS delay before send
	Rs485DelayRtsBeforeSend int

	// RTS delay after send
	Rs485DelayRtsAfterSend int
}

// Open creates an io.ReadWriteCloser based on the supplied options struct.
func Open(options OpenOptions) (io.ReadWriteCloser, error) {
	// Redirect to the OS-specific function.
	return openInternal(options)
}

// Rounds a float to the nearest integer.
func round(f float64) float64 {
	return math.Floor(f + 0.5)
}
