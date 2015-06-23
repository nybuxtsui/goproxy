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
	"time"

	"github.com/BurntSushi/toml"
)

var Config struct {
	Listen  string `toml:"listen"`
	Channel []struct {
		Name    string   `toml:"name"`
		Type    string   `toml:"type"`
		Addr    string   `toml:"addr"`
		Domains []string `toml:"domains"`
	} `toml:"channel"`
	Default struct {
		Type string `toml:"type"`
		Addr string `toml:"addr"`
	} `toml:"default"`
}

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

func indomains(domain string, port uint16) (net.Conn, error) {
	for _, channel := range Config.Channel {
		for _, d := range channel.Domains {
			if d[1:] == domain || strings.HasSuffix(domain, d) {
				log.Println(channel.Name, ":", domain)
				if channel.Type == "socks5" {
					return connectSocks5(channel.Addr, domain, port)
				} else {
					return net.Dial("tcp", fmt.Sprintf("%s:%v", domain, port))
				}
			}
		}
	}
	log.Println("default :", domain)
	if Config.Default.Type == "socks5" {
		return connectSocks5(Config.Default.Addr, domain, port)
	} else {
		return net.Dial("tcp", fmt.Sprintf("%s:%v", domain, port))
	}
}

func connectSocks5(socks, domain string, port uint16) (net.Conn, error) {
	c2, err := net.Dial("tcp", socks)
	if err != nil {
		log.Println("Conn.Dial failed:", err, socks)
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
		domain := string(buff[8 : 8+p2])
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
		c2, err = indomains(domain, uint16(port))
		if err != nil {
			log.Println("connect socks5 failed:", err)
			return
		}
		defer c2.Close()
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	} else {
		p1 := bytes.Index(buff[:n], []byte("\r\nHost: "))
		if p1 == -1 {
			log.Println("http proxy format error, host not found")
			return
		}
		p2 := bytes.Index(buff[p1+8:n], []byte("\r\n"))
		if p2 == -1 {
			log.Println("http proxy format error, host not finish")
			return
		}
		url := string(buff[p1+8 : p1+8+p2])
		p3 := strings.IndexByte(url, ':')
		port := 80
		_port := "80"
		domain := url
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
		c2, err = indomains(domain, uint16(port))
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

	go doPeer(c, c2)

	_, err = io.Copy(c2, c)
	if err != nil {
		return
	}
}

func doPeer(c1 net.Conn, c2 net.Conn) {
	defer c1.Close()
	defer c2.Close()

	_, err := io.Copy(c1, c2)
	if err != nil {
		return
	}
}
