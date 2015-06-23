package main

import (
	"bytes"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

func main() {
	l, err := net.Listen("tcp", "0.0.0.0:8080")
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
		domain := buff[8 : 8+p2]
		p3 := bytes.IndexByte(buff[8+p2+1:n], ' ')
		if p3 == -1 {
			log.Println("http proxy format error, ' ' not found")
			return
		}
		_port := buff[8+p2+1 : 8+p2+1+p3]
		c2, err = net.Dial("tcp", "127.0.0.1:11081")
		if err != nil {
			log.Println("Conn.Dial failed:", err)
			return
		}
		c2.SetDeadline(time.Now().Add(10 * time.Second))
		c2.Write([]byte{5, 1, 0})
		resp := make([]byte, 2)
		n, err := c2.Read(resp)
		if err != nil {
			log.Println("Conn.Read failed:", err)
			return
		}
		if n != 2 {
			log.Println("socks5 response error:", resp)
			return
		}
		if resp[1] != 0 {
			log.Println("socks5 not support 'NO AUTHENTICATION REQUIRED'")
			return
		}
		send := make([]byte, 0, 512)
		send = append(send, []byte{5, 1, 0, 3, byte(len(domain))}...)
		send = append(send, []byte(domain)...)
		port, err := strconv.Atoi(string(_port))
		send = append(send, byte(port>>8))
		send = append(send, byte(port&0xff))
		_, err = c2.Write(send)
		if err != nil {
			log.Println("Conn.Write failed:", err)
			return
		}
		n, err = c2.Read(send[0:10])
		if err != nil {
			log.Println("Conn.Read failed:", err)
			return
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
			return
		}
		c2.SetDeadline(time.Time{})
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
		if p3 == -1 {
			url += ":80"
		}

		c2, err = net.Dial("tcp", url)
		if err != nil {
			log.Println("Conn.Dial failed:", err)
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
		log.Println("io.Copy failed:", err)
		return
	}
}

func doPeer(c1 net.Conn, c2 net.Conn) {
	defer c1.Close()
	defer c2.Close()

	_, err := io.Copy(c1, c2)
	if err != nil {
		log.Println("io.Copy failed:", err)
		return
	}
}
