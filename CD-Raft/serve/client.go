package main

import (
	RPC "KV-Raft/RPC"
	"bufio"
	"context"
	"flag"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"math"
	"math/rand"
	"net"
	"os"
	"runtime/trace"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	cluster  []string
	leaderId int
	mu       sync.Mutex
	connPool map[string]*grpc.ClientConn
}
type Client2 struct {
	writeMsg chan int32
	mu       sync.Mutex
}
type Request struct {
	Type  string
	Key   string
	Value string
}

func (ct *Client) zipfian(n int, s float64) int {
	var sum float64
	for i := 1; i <= n; i++ {
		sum += 1.0 / math.Pow(float64(i), s)
	}
	r := rand.Float64() * sum
	for i := 1; i <= n; i++ {
		r -= 1.0 / math.Pow(float64(i), s)
		if r <= 0 {
			return i // 返回 1 到 n 之间的随机键
		}
	}
	return n
}
func (ct *Client) randomizeRequests(numReads int, numWrites int, numKeys int, zipfExponent float64) []Request {
	var requests []Request

	// 添加读请求
	for i := 0; i < numReads; i++ {
		key := strconv.Itoa(ct.zipfian(numKeys, zipfExponent)) // 生成的键在 1 到 numKeys 之间
		requests = append(requests, Request{Type: "read", Key: key})
	}

	// 添加写请求
	for i := 0; i < numWrites; i++ {
		key := strconv.Itoa(ct.zipfian(numKeys, zipfExponent)) // 生成的键在 1 到 numKeys 之间
		value := generateString(1024)
		requests = append(requests, Request{Type: "write", Key: key, Value: value})
	}

	// 随机打乱请求顺序
	rand.Shuffle(len(requests), func(i, j int) {
		requests[i], requests[j] = requests[j], requests[i]
	})

	return requests
}
func (ct *Client) executeRequests(ip string, ip2 string, requests []Request) (int64, int64) {
	costw := int64(0)
	costr := int64(0)
	for _, req := range requests {
		if req.Type == "read" {
			fmt.Printf("读操作：读取键 %s\n", req.Key)
			// 调用读函数
			t1 := time.Now()
			ct.startReadRequest2(ip2, ip, req.Key)
			t2 := time.Since(t1).Milliseconds()
			costr += t2
		} else {
			fmt.Printf("写操作：写入键 %s，值 %s\n", req.Key, 1)
			// 调用写函数
			t1 := time.Now()
			ct.startWriteRequest2(ip, req.Key, req.Value)
			t2 := time.Since(t1).Milliseconds()
			costw += t2
		}
	}
	return costw, costr
}
func (ct *Client) startWriteRequest2(ip string, key string, value string) {
	conn, err1 := grpc.Dial(ip+":40001", grpc.WithInsecure())
	if err1 != nil {
		fmt.Println(err1)
	}
	defer func() {
		err2 := conn.Close()
		if err2 != nil {
			fmt.Println(err2)
		}
	}()
	ct2 := RPC.NewClientClient(conn)
	timestamp := time.Now().UnixMilli()
	fmt.Printf("	·······start write %s-%s········\n", key, "1")
	if ip != "192.168.1.72" {
		//var wg sync.WaitGroup
		//wg.Add(1)
		go func() {
			ct.Write(key, value, timestamp, ip)
		}()
		ct2.Tell(context.Background(), &RPC.FastArgs{Give: 1})
	} else {
		ct.Write(key, value, timestamp, ip)
	}
}
func (ct *Client) startReadRequest2(ip string, ip2 string, key string) {
	fmt.Printf("	·······start read key：%s········\n", key)
	ct.Read(key, ip, ip2)
}

// 生成随机的字符串时需要用到
const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func NewClient(cluster []string) *Client {
	return &Client{
		cluster:  cluster,
		leaderId: 0,
		connPool: make(map[string]*grpc.ClientConn),
	}
}

func (ct *Client) getConn(address string) (*grpc.ClientConn, error) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if conn, ok := ct.connPool[address]; ok {
		return conn, nil
	}

	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	ct.connPool[address] = conn
	return conn, nil
}

func (ct *Client) sendWriteRequest(address string, args *RPC.WriteArgs) (bool, *RPC.WriteReply) {
	// WriteRequest 的 Client端 拨号
	conn, err1 := ct.getConn(address)
	if err1 != nil {
		fmt.Println(err1)
		return false, nil
	}
	client := RPC.NewServeClient(conn)
	//time1 := time.Now().UnixMilli()
	reply, err3 := client.WriteRequest(context.Background(), args)
	//time2 := time.Now().UnixMilli()
	//fmt.Println(time2-time1, "uuuu")
	if err3 != nil {
		fmt.Println(err3)
		return false, reply
	}
	return true, reply
}

func (ct *Client) sendReadRequest(address string, args *RPC.ReadArgs) (bool, *RPC.ReadReply) {
	// ReadRequest 的 Client端， 拨号
	conn, err1 := ct.getConn(address)
	if err1 != nil {
		fmt.Println(err1)
		return false, nil
	}

	client := RPC.NewServeClient(conn)
	reply, err3 := client.ReadRequest(context.Background(), args)
	if err3 != nil {
		fmt.Println(err3)
		return false, reply
	}
	return true, reply
}

func (ct *Client) Write(key, value string, times int64, ip string) {
	// 重定向到leader
	args := &RPC.WriteArgs{
		Key:   key,
		Value: value,
		Times: times,
		Ip:    ip,
	}
	id := ct.leaderId
	n := len(ct.cluster)
	for {
		ret, reply := ct.sendWriteRequest(ct.cluster[id], args)
		if ret {
			if !reply.IsLeader {
				fmt.Printf("Write请求，%s 不是Leader, id++\n", ct.cluster[id])
				id = (id + 1) % n
			} else {
				break //找到了leaderId，结束for
			}
		} else {
			fmt.Printf("send WriteRequest 返回false\n")
		}
	}
	ct.leaderId = id
}

//	func (ct *Client) Read(key string,ip string) *RPC.ReadReply {
//		//重定向到leader
//		args := &RPC.ReadArgs{Key: key}
//		id := ct.leaderId
//		n := len(ct.cluster)
//		var ret bool
//		var reply *RPC.ReadReply
//		for {
//			ret, reply = ct.sendReadRequest(ct.cluster[id], args)
//			if ret {
//				if !reply.IsLeader {
//					fmt.Printf("Read请求，%s 不是Leader, id++\n", ct.cluster[id])
//					id = (id + 1) % n
//				} else {
//					break
//				}
//			} else {
//				fmt.Printf("send ReadRequest 返回false\n")
//			}
//		}
//		ct.leaderId = id
//		return reply
//	}
func (ct *Client) Read(key string, ip string, ip2 string) *RPC.ReadReply {
	//重定向到leader
	args := &RPC.ReadArgs{
		Key: key,
		Ip:  ip2,
	}
	_, reply := ct.sendReadRequest(ip, args)

	return reply
}

func (ct *Client) load(ip string) {
	//conn, err1 := grpc.Dial("192.168.12.131:40001", grpc.WithInsecure())
	conn, err1 := grpc.Dial(ip+":40001", grpc.WithInsecure())
	if err1 != nil {
		fmt.Println(err1)
	}
	defer func() {
		err2 := conn.Close()
		if err2 != nil {
			fmt.Println(err2)
		}
	}()
	ct2 := RPC.NewClientClient(conn)
	timee := time.Now().UnixMilli()
	num := 1500 // 最简单5个write 5个read
	var key, value string
	for i := 0; i < num; i++ {
		timestamp := time.Now().UnixMilli()
		key = strconv.Itoa(i + 1)
		value = generateString(1024)
		fmt.Printf("	·······start write %s-%s········\n", key, "1")
		time1 := time.Now().UnixMilli()
		if ip != "192.168.1.72" {
			//var wg sync.WaitGroup
			//wg.Add(1)
			//done := make(chan bool)
			go func() {
				ct.Write(key, value, timestamp, ip)
				//done <- true
			}()
			//go func() {
			t1 := time.Now().UnixMilli()
			ct2.Tell(context.Background(), &RPC.FastArgs{Give: 1})
			t2 := time.Now().UnixMilli()
			fmt.Println(i+1, " 等待返回所花费时间：", t2-t1)
			//done <- true
			//}()
			//<-done
		} else {
			ct.Write(key, value, timestamp, ip)
		}
		time2 := time.Now().UnixMilli()
		fmt.Println("这次写所花费的时间", time2-time1)
		if i+1 == 100 || i+1 == 200 || i+1 == 500 || i+1 == 1000 || i+1 == 2000 || i+1 == 5000 || i+1 == 7000 || i+1 == 10000 {
			fmt.Println("第", i+1, "次写入所花费的时间", time2-timee)
		}
	}
}

func (ct *Client) startWriteRequest(ip string) {
	var result []int64
	//conn, err1 := grpc.Dial("192.168.12.131:40001", grpc.WithInsecure())
	conn, err1 := grpc.Dial(ip+":40001", grpc.WithInsecure())
	if err1 != nil {
		fmt.Println(err1)
	}
	defer func() {
		err2 := conn.Close()
		if err2 != nil {
			fmt.Println(err2)
		}
	}()
	ct2 := RPC.NewClientClient(conn)
	timee := time.Now().UnixMilli()
	num := 9 // 最简单5个write 5个read
	var key, value string
	for i := 0; i < num; i++ {
		timestamp := time.Now().UnixMilli()
		key = strconv.Itoa(i + 1)
		value = generateString(1024)
		//value = strconv.Itoa(i + 1)
		fmt.Printf("	·······start write %s-%s········\n", key, "1")
		time1 := time.Now().UnixMilli()
		if ip != "192.168.1.72" {
			var wg sync.WaitGroup
			wg.Add(1)
			//done := make(chan bool)
			go func() {
				ct.Write(key, value, timestamp, ip)
				//done <- true
			}()
			//go func() {
			t1 := time.Now().UnixMilli()
			ct2.Tell(context.Background(), &RPC.FastArgs{Give: 1})
			t2 := time.Now().UnixMilli()
			fmt.Println(i+1, " 等待返回所花费时间：", t2-t1)
			//done <- true
			//}()
			//<-done
		} else {
			ct.Write(key, value, timestamp, ip)
		}
		time2 := time.Now().UnixMilli()
		fmt.Println("这次写所花费的时间", time2-time1)
		if i+1 == 100 || i+1 == 200 || i+1 == 500 || i+1 == 1000 || i+1 == 2000 || i+1 == 5000 || i+1 == 7000 || i+1 == 9500 || i+1 == 10000 {
			fmt.Println("第", i+1, "次写入所花费的时间", time2-timee)
			result = append(result, time2-timee)
		}
	}
	err := writeResultsToFile("read.txt", result)
	if err != nil {
		fmt.Println("Error writing results to file:", err)
	} else {
		fmt.Println("Successfully wrote results to file")
	}
}

func (ct *Client) startReadRequest(ip string, ip2 string) {
	num := 1 // 最简单5个write 5个read
	var result []int64
	var key string
	t1 := time.Now().UnixMilli()
	for i := 0; i < num; i++ {
		key = strconv.Itoa((i + 1) % 500)
		fmt.Printf("	·······start read key：%s········\n", key)
		ct.Read(key, ip, ip2)
		t2 := time.Now().UnixMilli()
		if i+1 == 100 || i+1 == 200 || i+1 == 500 || i+1 == 1000 || i+1 == 2000 || i+1 == 5000 || i+1 == 7000 || i+1 == 9500 || i+1 == 10000 {
			fmt.Println("第", i+1, "次写入所花费的时间", t2-t1)
			result = append(result, t2-t1)
		}
		fmt.Println("1")
	}
	err := writeResultsToFile("read.txt", result)
	if err != nil {
		fmt.Println("Error writing results to file:", err)
	} else {
		fmt.Println("Successfully wrote results to file")
	}
}

func main() {
	f, err := os.Create("trace.out")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	err = trace.Start(f)
	if err != nil {
		panic(err)
	}
	defer trace.Stop()
	// Your program here

	var clu = flag.String("cluster", "", "all cluster members' address")
	var ip = flag.String("ip", "", "ip is this")
	var read = flag.String("read", "", "you can from this ip to read value")
	flag.Parse()
	cluster := strings.Split(*clu, ",")
	fmt.Println("集群成员：")
	n := len(cluster)
	for i := 0; i < n; i++ {
		cluster[i] = cluster[i] + "1"
		fmt.Println(cluster[i])
	}
	ct := NewClient(cluster)
	ct2 := &Client2{
		writeMsg: make(chan int32), // 使用带缓冲的通道避免阻塞
	}
	//go ct2.registerClient("192.168.12.131:40001")
	go ct2.registerClient(*ip + ":40001")
	fmt.Println(" ---------------start load----------------")
	t1 := time.Now()
	ct.load(*ip)
	t2 := time.Since(t1).Milliseconds()
	fmt.Println("load cost : ", t2)
	numw := int64(0)
	numr := int64(0)
	rand.Seed(time.Now().UnixNano()) // 初始化随机数生成器
	numReads := 1500                 // 指定读请求的数量
	numWrites := 1500                // 指定写请求的数量
	numKeys := 2000                  // 假设有 1 到 500 的键
	zipfExponent := 1.0              // Zipf 分布的偏度参数
	requests := ct.randomizeRequests(numReads, numWrites, numKeys, zipfExponent)
	numw, numr = ct.executeRequests(*ip, *read, requests)
	//time5 := time.Since(time1).Milliseconds()
	fmt.Println("Load cost : ", t2)
	fmt.Println("write cost : ", numw)
	fmt.Println("read cost : ", numr)
	fmt.Println("All cost : ", numw+numr)
}

func (ct2 *Client2) registerClient(address string) {
	lis, err1 := net.Listen("tcp", address)
	if err1 != nil {
		fmt.Println(err1)
	}
	server := grpc.NewServer()
	RPC.RegisterClientServer(server, ct2)
	err2 := server.Serve(lis)
	if err2 != nil {
		fmt.Println(err2)
	}
}

func (ct2 *Client2) Tell(ctx context.Context, args *RPC.FastArgs) (*RPC.FastReply, error) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	t1 := time.Now().UnixMilli()
	for {
		//time.Sleep(time.Millisecond * 1)
		//t1 := time.Now().UnixMilli()
		flg := false

		select {
		case <-ct2.writeMsg:
			fmt.Println("从通道中读出")
			flg = true
		case <-ctx.Done():
			fmt.Println("超时拒绝读取")
			flg = true
			//default:
		}
		if flg {
			break
		}
		//t2 := time.Now().UnixMilli()
		//fmt.Println("这次循环执行时间是", t2-t1)
	}
	t2 := time.Now().UnixMilli()
	fmt.Println("进循环体查看，这个死循环会花费多久推出：", t2-t1)
	//close(ct2.writeMsg)
	return nil, status.Errorf(codes.Unimplemented, "method FastReturn not implemented")
}

func (ct2 *Client2) FastReturn(ctx context.Context, args *RPC.FastArgs) (*RPC.FastReply, error) {
	now := time.Now()
	if now.UnixMilli()-args.Times > 1500 {
		return nil, status.Errorf(codes.Unimplemented, "method FastReturn not implemented")
	}
	if ct2.writeMsg == nil {
		ct2.writeMsg = make(chan int32)
	}

	//fmt.Println(args.Give)
	ct2.writeMsg <- args.Give
	t := time.Now().UnixMilli()
	fmt.Println(args.Give, " 这次写入花费的时间", t-args.Times)
	return nil, status.Errorf(codes.Unimplemented, "method FastReturn not implemented")
}

func generateString(size int) string {
	rand.Seed(time.Now().UnixNano())
	result := make([]byte, size)
	for i := range result {
		result[i] = charset[rand.Intn(len(charset))]
	}
	return string(result)
}
func writeResultsToFile(filename string, results []int64) error {
	// 创建文件
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	// 创建一个带缓冲的 writer 来提高性能
	writer := bufio.NewWriter(file)

	// 遍历数组，将每个int64元素转换为字符串并写入文件
	for _, result := range results {
		// 使用strconv.FormatInt将int64转换为字符串
		_, err := writer.WriteString(strconv.FormatInt(result, 10) + "\n")
		if err != nil {
			return err
		}
	}

	// 确保所有缓冲区的数据都被写入文件
	err = writer.Flush()
	if err != nil {
		return err
	}

	return nil
}
