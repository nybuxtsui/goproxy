package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
)

type ChannelDefine struct {
	Name string `toml:"name"`
	Type string `toml:"type"`
	Addr string `toml:"addr"`
}

var sspos = 0
var ssaddrs []string
var sslock sync.RWMutex
var Config struct {
	Listen  string `toml:"listen"`
	Channel []struct {
		Domains []string `toml:"domains"`
		ChannelDefine
	} `toml:"channel"`
	Default ChannelDefine `toml:"default"`
}
var channelCache = make(map[string]*ChannelDefine)
var channelLock sync.RWMutex

func main() {
	Config.Listen = "127.0.0.1:8080"
	_, err := toml.DecodeFile("goproxy.conf", &Config)
	if err != nil {
		log.Fatalln("DecodeFile failed:", err)
	}
	for i, channel := range Config.Channel {
		for j, d := range channel.Domains {
			if !strings.HasPrefix(d, ".") {
				Config.Channel[i].Domains[j] = "." + d
			}
		}
	}

	l, err := net.Listen("tcp", Config.Listen)
	if err != nil {
		log.Fatalln("Listen failed:", err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			log.Println("Accept failed:", err)
			continue
		}
		go doProxy(c)
	}
}

func getConnectByChannel(channel ChannelDefine, domain string, port uint16) (net.Conn, bool, error) {
	log.Println(channel.Name, ":", domain)
	if strings.HasPrefix(channel.Type, "ss,") {
		parts := strings.SplitN(channel.Type, ",", 3)
		if len(parts) != 3 {
			log.Println("Config shadowsocks failed:", channel.Type)
			return nil, false, errors.New("config_error")
		}
		c, err := connectShadowSocks(parts[1], parts[2], channel.Addr, domain, port)
		return c, false, err
	} else if channel.Type == "socks5" {
		c, err := connectSocks5(channel.Addr, domain, port)
		return c, false, err
	} else if channel.Type == "http" {
		c, err := connectHttpProxy(channel.Addr, domain, port)
		return c, false, err
	} else {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%v", domain, port), time.Second*5)
		return c, true, err
	}
}

func connectShadowSocks(method, password, ssaddr string, domain string, port uint16) (net.Conn, error) {
	cipher, err := ss.NewCipher(method, password)
	if err != nil {
		log.Println("ss.NewCipher failed:", err)
		return nil, err
	}
	var once sync.Once
	once.Do(func() {
		ssaddrs = strings.Split(ssaddr, ",")
	})

	sslock.RLock()
	addr := ssaddrs[sspos]
	sslock.RUnlock()

	rawaddr := make([]byte, 0, 512)
	rawaddr = append(rawaddr, []byte{3, byte(len(domain))}...)
	rawaddr = append(rawaddr, []byte(domain)...)
	rawaddr = append(rawaddr, byte(port>>8))
	rawaddr = append(rawaddr, byte(port&0xff))
	c, err := ss.DialWithRawAddr(rawaddr, addr, cipher.Copy())
	if err != nil {
		sslock.Lock()
		sspos++
		sspos = sspos % len(ssaddrs)
		sslock.Unlock()
	}
	return c, err
}

func getProxyConnect(domain string, port uint16) (net.Conn, bool, error) {
	channelLock.RLock()
	channel, ok := channelCache[domain]
	channelLock.RUnlock()
	if ok {
		return getConnectByChannel(*channel, domain, port)
	}
	for _, channel := range Config.Channel {
		for _, d := range channel.Domains {
			if d[1:] == domain || strings.HasSuffix(domain, d) {
				channelLock.Lock()
				channelCache[domain] = &channel.ChannelDefine
				channelLock.Unlock()

				return getConnectByChannel(channel.ChannelDefine, domain, port)
			}
		}
	}
	return getConnectByChannel(Config.Default, domain, port)
}

func connectHttpProxy(http, domain string, port uint16) (net.Conn, error) {
	c2, err := net.Dial("tcp", http)
	if err != nil {
		log.Println("Conn.Dial failed:", err, http)
		return nil, err
	}
	c2.SetDeadline(time.Now().Add(10 * time.Second))
	c2.Write([]byte(fmt.Sprintf("CONNECT %v:%v HTTP/1.1\r\nHost: %v:%v\r\n\r\n", domain, port)))
	buff := make([]byte, 17, 256)
	c2.Read(buff)
	for !bytes.HasSuffix(buff, []byte("\r\n\r\n")) {
		ch := make([]byte, 1)
		_, err = c2.Read(ch)
		if err != nil {
			log.Println("Conn.Read failed:", err, http)
			return nil, err
		}
		buff = append(buff, ch[0])
		if len(buff) > 255 {
			log.Println("HTTP Proxy Connect failed: return too long")
			return nil, errors.New("http_proxy_failed")
		}
	}
	if buff[9] != '2' {
		log.Println("HTTP Proxy Connect failed:", string(buff))
		return nil, errors.New("http_proxy_failed")
	}
	c2.SetDeadline(time.Time{})
	return c2, nil
}

func connectSocks5(socks5, domain string, port uint16) (net.Conn, error) {
	c2, err := net.Dial("tcp", socks5)
	if err != nil {
		log.Println("Conn.Dial failed:", err, socks5)
		return nil, err
	}
	c2.SetDeadline(time.Now().Add(10 * time.Second))
	c2.Write([]byte{5, 1, 0})
	resp := make([]byte, 2)
	n, err := c2.Read(resp)
	if err != nil {
		log.Println("Conn.Read failed:", err)
		return nil, err
	}
	if n != 2 {
		log.Println("socks5 response error:", resp)
		return nil, errors.New("socks5_error")
	}
	if resp[1] != 0 {
		log.Println("socks5 not support 'NO AUTHENTICATION REQUIRED'")
		return nil, errors.New("socks5_error")
	}
	send := make([]byte, 0, 512)
	send = append(send, []byte{5, 1, 0, 3, byte(len(domain))}...)
	send = append(send, []byte(domain)...)
	send = append(send, byte(port>>8))
	send = append(send, byte(port&0xff))
	_, err = c2.Write(send)
	if err != nil {
		log.Println("Conn.Write failed:", err)
		return nil, err
	}
	n, err = c2.Read(send[0:10])
	if err != nil {
		log.Println("Conn.Read failed:", err)
		return nil, err
	}
	if send[1] != 0 {
		switch send[1] {
		case 1:
			log.Println("socks5 general SOCKS server failure")
		case 2:
			log.Println("socks5 connection not allowed by ruleset")
		case 3:
			log.Println("socks5 Network unreachable")
		case 4:
			log.Println("socks5 Host unreachable")
		case 5:
			log.Println("socks5 Connection refused")
		case 6:
			log.Println("socks5 TTL expired")
		case 7:
			log.Println("socks5 Command not supported")
		case 8:
			log.Println("socks5 Address type not supported")
		default:
			log.Println("socks5 Unknown eerror:", send[1])
		}
		return nil, errors.New("socks5_error")
	}
	c2.SetDeadline(time.Time{})
	return c2, nil
}

func doProxy(c net.Conn) {
	defer c.Close()

	buff := make([]byte, 16*1024)
	n, err := c.Read(buff)
	if err != nil {
		log.Println("Conn.Read failed:", err)
		return
	}
	if n < 10 {
		log.Println("first package too small")
		return
	}
	var c2 net.Conn
	var domain string
	var direct bool
	if bytes.HasPrefix(buff[:n], []byte("CONNECT ")) {
		if !bytes.HasSuffix(buff[:n], []byte("\r\n\r\n")) {
			log.Println("http proxy format error, not finished")
			return
		}
		p2 := bytes.IndexByte(buff[8:n], ':')
		if p2 == -1 {
			log.Println("http proxy format error, ':' not found")
			return
		}
		domain = string(buff[8 : 8+p2])
		p3 := bytes.IndexByte(buff[8+p2+1:n], ' ')
		if p3 == -1 {
			log.Println("http proxy format error, ' ' not found")
			return
		}
		_port := string(buff[8+p2+1 : 8+p2+1+p3])
		port, err := strconv.Atoi(_port)
		if err != nil {
			log.Println("http proxy port format error, ", err)
			return
		}
		c2, direct, err = getProxyConnect(domain, uint16(port))
		if err != nil {
			log.Println("connect failed:", err)
			return
		}
		defer c2.Close()
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	} else {
		p1 := bytes.Index(buff[:n], []byte("http://"))
		if p1 == -1 {
			log.Println("http proxy format error, host not found")
			return
		}
		p2 := bytes.Index(buff[p1+7:n], []byte("/"))
		if p2 == -1 {
			log.Println("http proxy format error, host not finish")
			return
		}
		url := string(buff[p1+7 : p1+7+p2])
		buff = append(buff[:p1], buff[p1+7+p2:]...)
		n -= (7 + p2)
		p3 := strings.IndexByte(url, ':')
		port := 80
		_port := "80"
		domain = url
		if p3 == -1 {
			url += ":80"
		} else {
			domain = url[:p3]
			_port = string(url[p3+1:])
			port, err = strconv.Atoi(_port)
			if err != nil {
				log.Println("http port format error:", _port)
				return
			}
		}
		c2, direct, err = getProxyConnect(domain, uint16(port))
		if err != nil {
			log.Println("connect socks5 failed:", err)
			return
		}
		defer c2.Close()
		_, err = c2.Write(buff[:n])
		if err != nil {
			log.Println("Conn.Write failed:", err)
			return
		}
	}
	_ = direct

	go func() {
		defer c2.Close()
		defer c.Close()
		io.Copy(c, c2)
	}()
	io.Copy(c2, c)
}
