/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019 WireGuard LLC. All Rights Reserved.
 */

package service

import (
	"errors"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/winipcfg"
	"golang.zx2c4.com/wireguard/windows/conf"
	"net"
	"os"
)

const (
	sockoptIP_UNICAST_IF   = 31
	sockoptIPV6_UNICAST_IF = 31
)

func bindSocketToMonitoredDefault(bind *NativeBind) error {
	defaultIface, err := winipcfg.DefaultInterface(winipcfg.AF_INET)
	if err != nil {
		return err
	}
	sysconn, err := bind.ipv4.SyscallConn()
	if err != nil {
		return err
	}
	err2 := sysconn.Control(func(fd uintptr) {
		err = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, sockoptIP_UNICAST_IF, int(htonl(defaultIface.Index)))
	})
	if err2 != nil {
		return err2
	}
	if err != nil {
		return err
	}
	defaultIface, err = winipcfg.DefaultInterface(winipcfg.AF_INET6)
	if err != nil {
		return err
	}
	sysconn, err = bind.ipv6.SyscallConn()
	if err != nil {
		return err
	}
	err2 = sysconn.Control(func(fd uintptr) {
		err = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, sockoptIPV6_UNICAST_IF, int(defaultIface.Ipv6IfIndex))
	})
	if err2 != nil {
		return err2
	}
	if err != nil {
		return err
	}

	return nil

	//TODO: monitor for changes, and make sure we're using default modulo us
}

func configureInterface(conf *conf.Config, guid *windows.GUID) error {
	iface, err := winipcfg.InterfaceFromGUID(guid)
	if err != nil {
		return err
	}

	routeCount := len(conf.Interface.Addresses)
	for _, peer := range conf.Peers {
		routeCount += len(peer.AllowedIPs)
	}
	routes := make([]winipcfg.RouteData, routeCount)
	routeCount = 0
	var firstGateway4 *net.IP
	var firstGateway6 *net.IP
	addresses := make([]*net.IPNet, len(conf.Interface.Addresses))
	for i, addr := range conf.Interface.Addresses {
		ipnet := addr.IPNet()
		addresses[i] = &ipnet
		gateway := ipnet.IP.Mask(ipnet.Mask)
		if addr.Bits() == 32 && firstGateway4 == nil {
			firstGateway4 = &gateway
		} else if addr.Bits() == 128 && firstGateway6 == nil {
			firstGateway6 = &gateway
		}
		routes[routeCount] = winipcfg.RouteData{
			Destination: net.IPNet{
				IP:   gateway,
				Mask: ipnet.Mask,
			},
			NextHop: gateway,
			Metric:  0,
		}
		routeCount++
	}

	foundDefault4 := false
	foundDefault6 := false
	for _, peer := range conf.Peers {
		for _, allowedip := range peer.AllowedIPs {
			if (allowedip.Bits() == 32 && firstGateway4 == nil) || (allowedip.Bits() == 128 && firstGateway6 == nil) {
				return errors.New("Due to a Windows limitation, one cannot have interface routes without an interface address")
			}
			routes[routeCount] = winipcfg.RouteData{
				Destination: allowedip.IPNet(),
				Metric:      0,
			}
			if allowedip.Bits() == 32 {
				if allowedip.Cidr == 0 {
					foundDefault4 = true
				}
				routes[routeCount].NextHop = *firstGateway4
			} else if allowedip.Bits() == 128 {
				if allowedip.Cidr == 0 {
					foundDefault6 = true
				}
				routes[routeCount].NextHop = *firstGateway6
			}
			routeCount++
		}
	}

	err = iface.SetAddresses(addresses)
	if err != nil {
		return err
	}

	err = iface.FlushRoutes()
	if err != nil {
		return nil
	}
	for _, route := range routes {
		err = iface.AddRoute(&route, false)

		//TODO: Ignoring duplicate errors like this maybe isn't very reasonable.
		// instead we should make sure we're not adding duplicates ourselves when
		// inserting the gateway routes.
		if syserr, ok := err.(*os.SyscallError); ok {
			if syserr.Err == windows.Errno(ERROR_OBJECT_ALREADY_EXISTS) {
				err = nil
			}
		}

		if err != nil {
			return err
		}
	}

	err = iface.SetDNS(conf.Interface.Dns)
	if err != nil {
		return err
	}

	ipif, err := iface.GetIpInterface(winipcfg.AF_INET)
	if err != nil {
		return err
	}
	if foundDefault4 {
		ipif.UseAutomaticMetric = false
		ipif.Metric = 0
	}
	err = ipif.Set()
	if err != nil {
		return err
	}

	ipif, err = iface.GetIpInterface(winipcfg.AF_INET6)
	if err != nil {
		return err
	}
	if foundDefault6 {
		ipif.UseAutomaticMetric = false
		ipif.Metric = 0
	}
	ipif.DadTransmits = 0
	ipif.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
	err = ipif.Set()
	if err != nil {
		return err
	}

	return nil
}
