# 阶段 1: 编译
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .

# 禁用 CGO 以生成静态二进制文件
RUN CGO_ENABLED=0 GOOS=linux go build -o hlds-master main.go

# 阶段 2: 运行环境
FROM alpine:latest

WORKDIR /root/

# 从 builder 阶段复制编译好的二进制文件
COPY --from=builder /app/hlds-master .

# 暴露端口：27010 (UDP Master), 8080 (Web)
EXPOSE 27010/udp
EXPOSE 8080/tcp

# 运行
CMD ["./hlds-master"]
