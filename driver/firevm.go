/* Firecracker-task-driver is a task driver for Hashicorp's nomad that allows
 * to create microvms using AWS Firecracker vmm
 * Copyright (C) 2019  Carlos Neira cneirabustos@gmail.com
 *
 * Licensed under the Apache License, Version 2.0 (the "License")
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 */

package firevm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/containerd/console"
	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins/drivers"
	log "github.com/sirupsen/logrus"
)

const (
	// executableMask is the mask needed to check whether or not a file's
	// permissions are executable.
	executableMask = 0111

	// containerMonitorIntv is the interval at which the driver checks if the
	// firecracker micro-vm is still running
	containerMonitorIntv = 2 * time.Second
	defaultbootoptions   = " console=ttyS0 reboot=k panic=1 pci=off nomodules"
)

func taskConfig2FirecrackerOpts(taskConfig TaskConfig, cfg *drivers.TaskConfig) (*options, error) {
	opts := newOptions()

	if len(taskConfig.KernelImage) > 0 {
		opts.FcKernelImage = taskConfig.KernelImage
	} else {
		opts.FcKernelImage = filepath.Join(cfg.AllocDir, cfg.Name) + "/vmlinux"
	}

	if len(taskConfig.BootDisk) > 0 {
		opts.FcRootDrivePath = taskConfig.BootDisk
	} else {
		opts.FcRootDrivePath = filepath.Join(cfg.AllocDir, cfg.Name) + "/rootfs.ext4"
	}

	if len(taskConfig.Disks) > 0 {
		opts.FcAdditionalDrives = taskConfig.Disks
	}

	if len(taskConfig.BootOptions) > 0 {
		opts.FcKernelCmdLine = taskConfig.BootOptions + defaultbootoptions
	} else {
		opts.FcKernelCmdLine = defaultbootoptions
	}

	if len(taskConfig.Nic.Ip) > 0 {
		opts.FcNicConfig = taskConfig.Nic
	}
	if len(taskConfig.Network) > 0 {
		opts.FcNetworkName = taskConfig.Network
	}

	if len(taskConfig.Log) > 0 {
		opts.FcFifoLogFile = taskConfig.Log
		opts.Debug = true
		opts.FcLogLevel = "Debug"
	}

	if cfg.Resources.NomadResources.Cpu.CpuShares > 100 {
		opts.FcCPUCount = cfg.Resources.NomadResources.Cpu.CpuShares / 100
	} else {
		opts.FcCPUCount = 1
	}
	opts.FcCPUTemplate = taskConfig.Cputype
	opts.FcDisableHt = taskConfig.DisableHt

	if cfg.Resources.NomadResources.Memory.MemoryMB > 0 {
		opts.FcMemSz = cfg.Resources.NomadResources.Memory.MemoryMB
	} else {
		opts.FcMemSz = 300
	}
	opts.FcBinary = taskConfig.Firecracker

	return opts, nil
}

type vminfo struct {
	Machine *firecracker.Machine
	tty     string
	Console console.Console
	Info    Instance_info
	Cancel  context.CancelFunc
}

type Instance_info struct {
	AllocId string
	Ip      string
	Serial  string
	Pid     string
	Vnic    string
}

func (d *Driver) initializeContainer(ctx context.Context, cfg *drivers.TaskConfig, taskConfig TaskConfig) (*vminfo, error) {
	opts, _ := taskConfig2FirecrackerOpts(taskConfig, cfg)
	fcCfg, err := opts.getFirecrackerConfig(cfg.AllocID)
	if err != nil {
		log.Errorf("Error: %s", err)
		return nil, err
	}

	d.logger.Info("Starting firecracker", "driver_initialize_container", hclog.Fmt("%v+", opts))
	logger := log.New()

	if opts.Debug {
		log.SetLevel(log.DebugLevel)
		logger.SetLevel(log.DebugLevel)
	}

	var ok bool
	vmmCtx, vmmCancel := context.WithCancel(ctx)
	defer func() {
		if !ok {
			d.logger.Info("cancelling firecracker instance")
			vmmCancel()
		}
	}()

	machineOpts := []firecracker.Opt{
		firecracker.WithLogger(log.NewEntry(logger)),
	}

	fcenv := os.Getenv("FIRECRACKER_BIN")
	var firecrackerBinary string
	if len(opts.FcBinary) > 0 {
		firecrackerBinary = opts.FcBinary
	} else if len(fcenv) > 0 {
		firecrackerBinary = fcenv
	} else {
		firecrackerBinary = "/usr/bin/firecracker"
	}
	d.logger.Info("found firecracker executable", "filename", firecrackerBinary)

	finfo, err := os.Stat(firecrackerBinary)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("Binary %q does not exist: %v", firecrackerBinary, err)
	}

	if err != nil {
		return nil, fmt.Errorf("Failed to stat binary, %q: %v", firecrackerBinary, err)
	}

	if finfo.IsDir() {
		return nil, fmt.Errorf("Binary, %q, is a directory", firecrackerBinary)
	} else if finfo.Mode()&executableMask == 0 {
		return nil, fmt.Errorf("Binary, %q, is not executable. Check permissions of binary", firecrackerBinary)
	}

	d.logger.Info("create firecracker console")
	tty, ftty, err := console.NewPty()

	if err != nil {
		return nil, fmt.Errorf("Could not create serial console  %v+", err)
	}
	d.logger.Info("create console console created", "slave", ftty, "fd", tty.Fd(), "name", tty.Name())

	cmd := firecracker.VMCommandBuilder{}.
		WithBin(firecrackerBinary).
		WithSocketPath(fcCfg.SocketPath).
		WithStdin(tty).
		WithStdout(tty).
		WithStderr(tty).
		Build(ctx)

	machineOpts = append(machineOpts, firecracker.WithProcessRunner(cmd))

	d.logger.Info("create firecracker machine")
	m, err := firecracker.NewMachine(vmmCtx, fcCfg, machineOpts...)
	if err != nil {
		return nil, fmt.Errorf("Failed creating machine: %v", err)
	}

	d.logger.Info("start firecracker machine")
	if err := m.Start(vmmCtx); err != nil {
		return nil, fmt.Errorf("Failed to start machine: %v", err)
	}

	if opts.validMetadata != nil {
		err = m.SetMetadata(vmmCtx, opts.validMetadata)
		d.logger.Error("failed to set metadata on machine", "Err", err)
	}

	d.logger.Info("get firecracker machine pid")
	pid, errpid := m.PID()
	if errpid != nil {
		return nil, fmt.Errorf("Failed getting pid for machine: %v", errpid)
	}
	d.logger.Info("firecracker machine started", "PID", pid)
	var ip string
	var vnic string
	if len(opts.FcNetworkName) > 0 {
		ip = fcCfg.NetworkInterfaces[0].StaticConfiguration.IPConfiguration.IPAddr.String()
		vnic = fcCfg.NetworkInterfaces[0].CNIConfiguration.IfName + "vm"
	} else {
		ip = "No network chosen"
		vnic = ip
	}
	info := Instance_info{Serial: ftty, AllocId: cfg.AllocID,
		Ip:  ip,
		Pid: strconv.Itoa(pid), Vnic: vnic}

	f, _ := json.MarshalIndent(info, "", " ")

	logfile := fmt.Sprintf("/tmp/%s-%s", cfg.Name, cfg.AllocID)

	d.logger.Info("Writing to", "driver_initialize_container", hclog.Fmt("%v+", logfile))
	log, err := os.OpenFile(logfile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)

	if err != nil {
		return nil, fmt.Errorf("Failed creating info file=%s err=%v", logfile, err)
	}
	defer log.Close()
	fmt.Fprintf(log, "%s", f)

	ok = true
	return &vminfo{Machine: m, tty: ftty, Info: info, Console: tty, Cancel: vmmCancel}, nil
}

// tickle tries to open and write to the given pty
// the point of it being to force it to break the
// read function that is blocked on it in the driver
func tickle(pty string) {
	w, err := os.OpenFile(pty, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		log.Printf("failed to open pty %q -- %v", pty, err)
		return
	}
	_, err = w.WriteString("exit")
	if err != nil {
		log.Printf("failed to write to pty %q -- %v", pty, err)
		return
	}
	w.Close()
}
