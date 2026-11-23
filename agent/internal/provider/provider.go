package provider

import (
	"context"
	"errors"
)

var ErrVMNotFound = errors.New("vm not found")

type VM struct {
	ID           string `json:"id"`
	VMID         int    `json:"vmid,omitempty"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Cores        int    `json:"cores"`
	MemoryMB     int    `json:"memory_mb"`
	DiskGB       int    `json:"disk_gb"`
	SystemDiskGB int    `json:"system_disk_gb"`
	DataDiskGB   int    `json:"data_disk_gb"`
	OSType       string `json:"ostype,omitempty"`
	IP           string `json:"ip,omitempty"`
	Node         string `json:"node"`
	Uptime       int64  `json:"uptime"`
	Password     string `json:"password,omitempty"`
}

type CreateVMInput struct {
	Name         string `json:"name"`
	Cores        int    `json:"cores"`
	MemoryMB     int    `json:"memory_mb"`
	DiskGB       int    `json:"disk_gb"`
	DataDiskGB   int    `json:"data_disk_gb"`
	Storage      string `json:"storage"`
	Bridge       string `json:"bridge"`
	OSType       string `json:"ostype"`
	Image        string `json:"image"`
	Template     string `json:"template"`
	IP           string `json:"ip"`
	Gateway      string `json:"gateway"`
	PrefixLen    int    `json:"prefix_len"`
	NetworkMode  string `json:"network_mode"`
	SSHPublicKey string `json:"ssh_public_key"`
	Password     string `json:"password"`

	SystemDiskMinMBps int    `json:"system_disk_min_mbps"`
	SystemDiskMaxMBps int    `json:"system_disk_max_mbps"`
	DataDiskMinMBps   int    `json:"data_disk_min_mbps"`
	DataDiskMaxMBps   int    `json:"data_disk_max_mbps"`
	TemplateDir       string `json:"template_dir"`
	SystemDiskDir     string `json:"system_disk_dir"`
	DataDiskDir       string `json:"data_disk_dir"`
	ISODir            string `json:"iso_dir"`
	ISOFile           string `json:"iso_file"`
	BackupDir         string `json:"backup_dir"`
	DNS1              string `json:"dns1"`
	DNS2              string `json:"dns2"`
	PortMin           int    `json:"port_min"`
	PortMax           int    `json:"port_max"`
	ExcludePorts      string `json:"exclude_ports"`

	Start bool `json:"start"`
}

type Provider interface {
	Name() string
	List(ctx context.Context) ([]VM, error)
	Create(ctx context.Context, input CreateVMInput) (VM, error)
	Status(ctx context.Context, id string) (VM, error)
	Action(ctx context.Context, id string, action string) error
	Delete(ctx context.Context, id string) error
}

type Backuper interface {
	Backup(ctx context.Context, id string, destination string) (string, error)
}

type VNCInfo struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Display string `json:"display"`
}

type VNCProvider interface {
	VNC(ctx context.Context, id string) (VNCInfo, error)
}

type VMStats struct {
	CPUTimeNS         uint64 `json:"cpu_time_ns"`
	MemoryMB          int    `json:"memory_mb"`
	MemoryTotalMB     int    `json:"memory_total_mb"`
	MemoryUsedMB      int    `json:"memory_used_mb"`
	MemoryAvailableMB int    `json:"memory_available_mb"`
	NetRxBytes        uint64 `json:"net_rx_bytes"`
	NetTxBytes        uint64 `json:"net_tx_bytes"`
	DiskRDBytes       uint64 `json:"disk_rd_bytes"`
	DiskWRBytes       uint64 `json:"disk_wr_bytes"`
}

type StatsProvider interface {
	Stats(ctx context.Context, id string) (VMStats, error)
}

type BandwidthProvider interface {
	SetBandwidth(ctx context.Context, id string, uploadMbps int, downloadMbps int) error
}

type ReinstallProvider interface {
	Reinstall(ctx context.Context, id string, image string, isoFile string, ostype string) (VM, error)
}

type BootOrderProvider interface {
	SetBootOrder(ctx context.Context, id string, order []string, isoFile string) error
}

var ErrTemplateInUse = errors.New("template is in use by a virtual machine")

type TemplateInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	OSType    string `json:"ostype,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
}

type TemplateProvider interface {
	CreateTemplate(ctx context.Context, id string, name string) (TemplateInfo, error)
	DeleteTemplate(ctx context.Context, path string) error
}
type CDROMInfo struct {
	Mounted bool   `json:"mounted"`
	Target  string `json:"target"`
	ISOFile string `json:"iso_file"`
}

type CDROMProvider interface {
	CDROM(ctx context.Context, id string) (CDROMInfo, error)
	MountISO(ctx context.Context, id string, isoFile string) error
	EjectISO(ctx context.Context, id string) error
}

type KeyProvider interface {
	SendKey(ctx context.Context, id string, key string) error
}

type Diagnostic struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Diagnoser interface {
	Diagnose(ctx context.Context) []Diagnostic
}
