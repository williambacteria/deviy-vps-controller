package controller

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

func (c *Controller) findPool(poolID string) *IPPool {
	for index := range c.ipPools {
		if c.ipPools[index].ID == poolID {
			return &c.ipPools[index]
		}
	}

	return nil
}

func (c *Controller) allocateIPLocked(poolID, requested string) (string, string, int, error) {
	pool := c.findPool(poolID)
	if pool == nil {
		return "", "", 0, errors.New("IP 池不存在")
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(pool.CIDR))
	if err != nil || !prefix.Addr().Is4() {
		return "", "", 0, errors.New("IP 池 CIDR 无效")
	}
	prefix = prefix.Masked()
	start, err := poolStart(pool, prefix)
	if err != nil {
		return "", "", 0, err
	}
	end, err := poolEnd(pool, prefix)
	if err != nil {
		return "", "", 0, err
	}
	if start.Compare(end) > 0 {
		return "", "", 0, errors.New("IP 池起止地址无效")
	}

	used := make(map[string]bool, len(pool.Allocations))
	for _, ip := range pool.Allocations {
		used[ip] = true
	}

	if requested != "" {
		addr, err := netip.ParseAddr(strings.TrimSpace(requested))
		if err != nil || !addr.Is4() || addr.Compare(start) < 0 || addr.Compare(end) > 0 || used[addr.String()] {
			return "", "", 0, errors.New("指定 IP 不在可用范围或已被占用")
		}
		pool.Allocations = append(pool.Allocations, addr.String())
		c.persistLocked()
		return addr.String(), pool.Gateway, prefix.Bits(), nil
	}

	for addr := start; addr.Compare(end) <= 0; addr = addr.Next() {
		if !addr.IsValid() {
			break
		}
		if used[addr.String()] {
			continue
		}
		pool.Allocations = append(pool.Allocations, addr.String())
		c.persistLocked()
		return addr.String(), pool.Gateway, prefix.Bits(), nil
	}

	return "", "", 0, errors.New("IP 池已耗尽")
}

func (c *Controller) releaseAllocationLocked(poolID, ip string) {
	pool := c.findPool(poolID)
	if pool == nil {
		return
	}
	for index, item := range pool.Allocations {
		if item == ip {
			pool.Allocations = append(pool.Allocations[:index], pool.Allocations[index+1:]...)
			c.persistLocked()
			return
		}
	}
}

func poolStart(pool *IPPool, prefix netip.Prefix) (netip.Addr, error) {
	if strings.TrimSpace(pool.Start) == "" {
		return prefix.Masked().Addr().Next(), nil
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(pool.Start))
	if err != nil || !addr.Is4() || !prefix.Contains(addr) {
		return netip.Addr{}, errors.New("IP 池起始地址无效")
	}

	return addr, nil
}

func poolEnd(pool *IPPool, prefix netip.Prefix) (netip.Addr, error) {
	if strings.TrimSpace(pool.End) != "" {
		addr, err := netip.ParseAddr(strings.TrimSpace(pool.End))
		if err != nil || !addr.Is4() || !prefix.Contains(addr) {
			return netip.Addr{}, errors.New("IP 池结束地址无效")
		}
		return addr, nil
	}

	base := prefix.Masked().Addr().As4()
	value := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	last := value + (1 << (32 - prefix.Bits())) - 2
	return netip.AddrFrom4([4]byte{byte(last >> 24), byte(last >> 16), byte(last >> 8), byte(last)}), nil
}

func vmAllocationKey(nodeID, vmID string) string {
	return fmt.Sprintf("%s/%s", nodeID, vmID)
}
