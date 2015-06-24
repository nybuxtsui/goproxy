# goproxy
将socks5/http代理/shadowsocks代理转换成http代理
# 程序运行需要goproxy.conf配置文件
下面的例子表示：
- 代理在0.0.0.0:18080监听
- 所有规则都没匹配上，则走socks5代理的127.0.0.1:11080，如果没有配置这一项，则表示直接通过本地访问
- channel代表了不同的访问规则
- 这里配置了一个规则，符合规则的域名，走socks5代理的127.0.0.1:11081

# type支持的格式
- socks5 表示启用socks5代理，addr为代理地址
- http 表示启用http代理，addr为代理地址
- 没有 表示服务器直接代理
- ss,<加密算法>,<密钥> 表示使用shadowsocks代理，addr为用逗号隔开的多个shadowsocks服务器地址


```
listen = "0.0.0.0:18080"
[default]
type = "socks5"
addr = "127.0.0.1:11080"

[[channel]]
name = "socks5"
type = "socks5"
addr = "127.0.0.1:11081"
domains = [
    "google.com",
]
```
