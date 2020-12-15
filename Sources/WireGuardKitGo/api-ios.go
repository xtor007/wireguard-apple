/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2018-2019 Jason A. Donenfeld <Jason@zx2c4.com>. All Rights Reserved.
 */

package main

// #include <stdlib.h>
// #include <sys/types.h>
// static void callLogger(void *func, void *ctx, int level, const char *msg)
// {
// 	((void(*)(void *, int, const char *))func)(ctx, level, msg);
// }
import "C"

import (
	"bufio"
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"log"
	"math"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"unsafe"
)

var loggerFunc unsafe.Pointer
var loggerCtx unsafe.Pointer
var versionString *C.char

type CLogger struct {
	level C.int
}

func (l *CLogger) Write(p []byte) (int, error) {
	if uintptr(loggerFunc) == 0 {
		return 0, errors.New("No logger initialized")
	}
	message := C.CString(string(p))
	C.callLogger(loggerFunc, loggerCtx, l.level, message)
	C.free(unsafe.Pointer(message))
	return len(p), nil
}

type tunnelHandle struct {
	*device.Device
	*device.Logger
}

var tunnelHandles = make(map[int32]tunnelHandle)

func init() {
	versionString = C.CString(device.WireGuardGoVersion)
	signals := make(chan os.Signal)
	signal.Notify(signals, unix.SIGUSR2)
	go func() {
		buf := make([]byte, os.Getpagesize())
		for {
			select {
			case <-signals:
				n := runtime.Stack(buf, true)
				buf[n] = 0
				if uintptr(loggerFunc) != 0 {
					C.callLogger(loggerFunc, loggerCtx, 0, (*C.char)(unsafe.Pointer(&buf[0])))
				}
			}
		}
	}()
}

//export wgSetLogger
func wgSetLogger(context, loggerFn uintptr) {
	loggerCtx = unsafe.Pointer(context)
	loggerFunc = unsafe.Pointer(loggerFn)
}

//export wgTurnOn
func wgTurnOn(settings *C.char, tunFd int32) int32 {
	logger := &device.Logger{
		Debug: log.New(&CLogger{level: 0}, "", 0),
		Info:  log.New(&CLogger{level: 1}, "", 0),
		Error: log.New(&CLogger{level: 2}, "", 0),
	}
	dupTunFd, err := unix.Dup(int(tunFd))
	if err != nil {
		logger.Error.Println(err)
		return -1
	}

	err = unix.SetNonblock(dupTunFd, true)
	if err != nil {
		logger.Error.Println(err)
		unix.Close(dupTunFd)
		return -1
	}
	tun, err := tun.CreateTUNFromFile(os.NewFile(uintptr(dupTunFd), "/dev/tun"), 0)
	if err != nil {
		logger.Error.Println(err)
		unix.Close(dupTunFd)
		return -1
	}
	logger.Info.Println("Attaching to interface")
	device := device.NewDevice(tun, logger)

	setError := device.IpcSetOperation(bufio.NewReader(strings.NewReader(C.GoString(settings))))
	if setError != nil {
		logger.Error.Println(setError)
		unix.Close(dupTunFd)
		return -1
	}

	device.Up()
	logger.Info.Println("Device started")

	var i int32
	for i = 0; i < math.MaxInt32; i++ {
		if _, exists := tunnelHandles[i]; !exists {
			break
		}
	}
	if i == math.MaxInt32 {
		unix.Close(dupTunFd)
		return -1
	}
	tunnelHandles[i] = tunnelHandle{device, logger}
	return i
}

//export wgTurnOff
func wgTurnOff(tunnelHandle int32) {
	device, ok := tunnelHandles[tunnelHandle]
	if !ok {
		return
	}
	delete(tunnelHandles, tunnelHandle)
	device.Close()
}

//export wgSetConfig
func wgSetConfig(tunnelHandle int32, settings *C.char) int64 {
	dev, ok := tunnelHandles[tunnelHandle]
	if !ok {
		return 0
	}
	err := dev.IpcSetOperation(bufio.NewReader(strings.NewReader(C.GoString(settings))))
	if err != nil {
		dev.Error.Println(err)
		if ipcErr, ok := err.(*device.IPCError); ok {
			return ipcErr.ErrorCode()
		}
		return -1
	}
	return 0
}

//export wgGetConfig
func wgGetConfig(tunnelHandle int32) *C.char {
	device, ok := tunnelHandles[tunnelHandle]
	if !ok {
		return nil
	}
	settings := new(bytes.Buffer)
	writer := bufio.NewWriter(settings)
	err := device.IpcGetOperation(writer)
	if err != nil {
		return nil
	}
	writer.Flush()
	return C.CString(settings.String())
}

//export wgBumpSockets
func wgBumpSockets(tunnelHandle int32) {
	device, ok := tunnelHandles[tunnelHandle]
	if !ok {
		return
	}
	device.BindUpdate()
	device.SendKeepalivesToPeersWithCurrentKeypair()
}

//export wgDisableSomeRoamingForBrokenMobileSemantics
func wgDisableSomeRoamingForBrokenMobileSemantics(tunnelHandle int32) {
	device, ok := tunnelHandles[tunnelHandle]
	if !ok {
		return
	}
	device.DisableSomeRoamingForBrokenMobileSemantics()
}

//export wgVersion
func wgVersion() *C.char {
	return versionString
}

func main() {}
