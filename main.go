package main

import (
	"bytes"
	"html/template"
	"log"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ServerInfo 存储服务器的基本信息和查询到的状态
type ServerInfo struct {
	Address    string
	LastSeen   time.Time
	Name       string
	Map        string
	Players    int
	MaxPlayers int
}

// ServerManager 管理服务器列表的并发安全
type ServerManager struct {
	servers map[string]*ServerInfo
	mu      sync.RWMutex
}

var manager = ServerManager{
	servers: make(map[string]*ServerInfo),
}

// HTML 模板
const htmlTemplate = `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>CS 1.6 Server List</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
    <style>body { padding: 20px; background-color: #f8f9fa; } .table { background: white; }</style>
</head>
<body>
    <div class="container">
        <h2 class="mb-4">在线 CS 服务器列表</h2>
        <div class="alert alert-info">当前在线服务器数量: {{ .Count }}</div>
        <table class="table table-striped table-hover border">
            <thead class="table-dark">
                <tr>
                    <th>服务器名称</th>
                    <th>地址 (IP:Port)</th>
                    <th>地图</th>
                    <th>人数</th>
                    <th>最后更新</th>
                </tr>
            </thead>
            <tbody>
                {{ range .Servers }}
                <tr>
                    <td>{{ .Name }}</td>
                    <td>{{ .Address }}</td>
                    <td>{{ .Map }}</td>
                    <td>{{ .Players }}/{{ .MaxPlayers }}</td>
                    <td>{{ .LastSeen.Format "15:04:05" }}</td>
                </tr>
                {{ end }}
            </tbody>
        </table>
        <div class="text-muted small">自动刷新中...</div>
    </div>
    <script>setTimeout(function(){ location.reload(); }, 10000);</script>
</body>
</html>
`

func main() {
	// 1. 启动 UDP Master Server 监听 (27010)
	go startUDPServer()

	// 2. 启动后台清理和查询任务
	go startCleanerAndQuery()

	// 3. 启动 Web 服务器 (8080)
	http.HandleFunc("/", handleWeb)
	log.Println("Web Server started on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// startUDPServer 处理来自游戏服务器的心跳包
func startUDPServer() {
	addr, _ := net.ResolveUDPAddr("udp", ":27010")
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("UDP Listen error: %v", err)
	}
	defer conn.Close()
	log.Println("Master Server (UDP) listening on :27010")

	buf := make([]byte, 1024)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		// HLDS 发送的心跳包通常包含 'q' 或 '1' (0x31) 等字节
		if n > 0 {
			registerServer(remoteAddr.String())
		}
	}
}

// registerServer 注册或更新服务器
func registerServer(address string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if s, exists := manager.servers[address]; exists {
		s.LastSeen = time.Now()
	} else {
		log.Printf("New server detected: %s", address)
		manager.servers[address] = &ServerInfo{
			Address:  address,
			LastSeen: time.Now(),
			Name:     "Scanning...",
		}
	}
}

// handleWeb 处理网页请求
func handleWeb(w http.ResponseWriter, r *http.Request) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	var list []*ServerInfo
	for _, s := range manager.servers {
		list = append(list, s)
	}
	// 排序
	sort.Slice(list, func(i, j int) bool {
		return list[i].LastSeen.After(list[j].LastSeen)
	})

	data := struct {
		Count   int
		Servers []*ServerInfo
	}{
		Count:   len(list),
		Servers: list,
	}

	tmpl, _ := template.New("list").Parse(htmlTemplate)
	tmpl.Execute(w, data)
}

// startCleanerAndQuery 定期清理离线服务器并查询在线服务器详情
func startCleanerAndQuery() {
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	for range ticker.C {
		manager.mu.Lock()
		// 复制一份需要处理的服务器地址，释放锁后再去查询网络，防止阻塞
		var checkList []string
		
		for addr, s := range manager.servers {
			// 1. 删除超过 5 分钟未发送心跳的服务器
			if time.Since(s.LastSeen) > 5*time.Minute {
				delete(manager.servers, addr)
				log.Printf("Server removed (timeout): %s", addr)
				continue
			}
			checkList = append(checkList, addr)
		}
		manager.mu.Unlock()

		// 2. 查询服务器详情 (A2S_INFO) - 并发查询
		for _, addr := range checkList {
			go func(targetAddr string) {
				queryServerDetails(targetAddr)
			}(addr)
		}
	}
}

// queryServerDetails 发送 A2S_INFO 查询
func queryServerDetails(address string) {
	conn, err := net.DialTimeout("udp", address, 3*time.Second)
	if err != nil {
		return
	}
	defer conn.Close()

	// A2S_INFO Header: 0xFF 0xFF 0xFF 0xFF + 'T' + Payload
	query := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54, 0x53, 0x6F, 0x75, 0x72, 0x63, 0x65, 0x20, 0x45, 0x6E, 0x67, 0x69, 0x6E, 0x65, 0x20, 0x51, 0x75, 0x65, 0x72, 0x79, 0x00}
	conn.Write(query)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	resp := make([]byte, 1400)
	n, err := conn.Read(resp)
	if err != nil || n < 5 {
		return
	}

	// 解析简单的 GoldSrc/Source 响应 (跳过 Header)
	buffer := bytes.NewBuffer(resp[5:]) // Skip FFFFFFFF + Header
	
	readString := func(b *bytes.Buffer) string {
		str, _ := b.ReadString(0x00)
		if len(str) > 0 {
			return str[:len(str)-1]
		}
		return ""
	}

	// 协议格式通常为: Protocol, Name, Map, Folder, Game, ID, Players, MaxPlayers...
	// 防止 buffer 溢出 panic
	defer func() {
		if r := recover(); r != nil {
			// 忽略解析错误
		}
	}()

	_ = buffer.Next(1) // Protocol version
	name := readString(buffer)
	mapName := readString(buffer)
	_ = readString(buffer) // Folder
	_ = readString(buffer) // Game
	_ = buffer.Next(2)     // ID
	
	// 简单的长度检查
	if buffer.Len() < 2 {
		return
	}
	players := int(buffer.Next(1)[0])
	maxPlayers := int(buffer.Next(1)[0])

	manager.mu.Lock()
	// 再次检查是否存在，避免并发删除问题
	if target, ok := manager.servers[address]; ok {
		target.Name = name
		target.Map = mapName
		target.Players = players
		target.MaxPlayers = maxPlayers
	}
	manager.mu.Unlock()
}
