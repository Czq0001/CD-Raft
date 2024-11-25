package main

import (
	RPC "KV-Raft/RPC"
	RAFT "KV-Raft/Raft"
	PERSISTER "KV-Raft/persist"
	"flag"
	"fmt"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	"strings"
	"sync"
	"time"
)

type Server struct {
	address string
	members []string
	mu      *sync.Mutex
	rf      *RAFT.Raft
	persist *PERSISTER.Persister
}

type Server2 struct {
	writeMsg chan int32
	mu       sync.Mutex
}

func (sv *Server) WriteRequest(ctx context.Context, args *RPC.WriteArgs) (*RPC.WriteReply, error) {

	fmt.Printf("\n·····1····进入%s的WriteRequest处理端·········\n", sv.address)
	reply := &RPC.WriteReply{}
	_, reply.IsLeader = sv.rf.GetState()
	if !reply.IsLeader {
		fmt.Printf("\n·········%s 不是leader，return false·········\n", sv.address)
		return reply, nil
	}
	request := RAFT.Op{
		Key:    args.Key,
		Value:  args.Value,
		Option: "write",
		Times:  args.Times,
		Ip:     args.Ip,
	}
	if args.Ip == "192.168.1.62" {
		time.Sleep(time.Millisecond * 40)
	} else if args.Ip == "192.168.1.76" {
		time.Sleep(time.Millisecond * 50)
	}
	index, _, isLeader := sv.rf.Start(request)
	if !isLeader {
		fmt.Printf("******* %s When write, Leader change!*****\n", sv.address)
		reply.IsLeader = false
		return reply, nil
	}
	//apply := <- sv.applyCh
	fmt.Printf("Server端--新指令的内容：%s %s %s, 新指令的index：%d\n", request.Option, request.Key, "1", index)
	reply.IsLeader = true
	reply.Success = true
	fmt.Printf("·····2····%s的WriteRequest处理成功·········\n", sv.address)
	if args.Ip == "192.168.1.62" {
		time.Sleep(time.Millisecond * 40)
	} else if args.Ip == "192.168.1.76" {
		time.Sleep(time.Millisecond * 50)
	}
	return reply, nil
}

func (sv *Server) ReadRequest(ctx context.Context, args *RPC.ReadArgs) (*RPC.ReadReply, error) {
	fmt.Printf("\n·····1····进入%s的ReadRequest处理端·········\n", sv.address)
	reply := &RPC.ReadReply{}
	//_, reply.IsLeader = sv.rf.GetState()
	//if !reply.IsLeader {
	//	fmt.Printf("\n·········%s 不是leader，return false·········\n", sv.address)
	//	return reply, nil
	//}
	request := RAFT.Op{
		Option: "read",
		Key:    args.Key,
	}
	//index, _, isLeader := sv.rf.Start(request)
	//if !isLeader {
	//	fmt.Printf("******* %s When read, Leader change!*****\n", sv.address)
	//	reply.IsLeader = false
	//	return reply, nil
	//}
	reply.IsLeader = true
	//apply := <- sv.applyCh
	//卧槽我居然直接写了
	//time.Sleep(time.Millisecond * 25)
	if args.Ip == "192.168.1.62" {
		time.Sleep(time.Millisecond * 40)
	} else if args.Ip == "192.168.1.76" {
		time.Sleep(time.Millisecond * 50)
	}
	//reply.Value = sv.rf.Persist.Get(args.Key)
	reply.Value = sv.rf.FastRead(args.Key)
	fmt.Printf("新指令的内容：%s %s", request.Option, request.Key)
	//读取的内容
	//fmt.Printf("读取到的内容：%s\n", reply.Value)
	fmt.Printf("·····2····%s的ReadRequest处理成功·········\n", sv.address)
	//time.Sleep(time)
	if args.Ip == "192.168.1.62" {
		time.Sleep(time.Millisecond * 40)
	} else if args.Ip == "192.168.1.76" {
		time.Sleep(time.Millisecond * 50)
	}
	return reply, nil
}

func (sv *Server) registerServer(address string) {
	// Client和集群成员交互 的Server端
	fmt.Println("················进入外部注册服务器········")
	lis, err1 := net.Listen("tcp", address)
	if err1 != nil {
		fmt.Println(err1)
	}
	server := grpc.NewServer()
	RPC.RegisterServeServer(server, sv)
	err2 := server.Serve(lis)
	if err2 != nil {
		fmt.Println(err2)
	}
}

func main() {
	var add = flag.String("address", "", "servers's address")
	var mems = flag.String("members", "", "other members' address")
	var layer = flag.Int("layer", 0, "layer")
	flag.Parse()

	ip := strings.Split(*add, ":")[0]

	address := *add
	members := strings.Split(*mems, ",")
	persist := &PERSISTER.Persister{}

	persist.Init("../db" + fmt.Sprintf("_%d", time.Now().Unix()))
	sv := Server{
		address: address,
		members: members,
		persist: persist,
	}
	sv2 := &Server2{
		writeMsg: make(chan int32),
	}
	if !isPortInUse(ip + ":50001") {
		go sv2.registerServer2(ip + ":50001")
	}
	go sv.registerServer(sv.address + "1")
	sv.rf = RAFT.MakeRaft(sv.address, sv.members, sv.persist, sv.mu, *layer)
	time.Sleep(time.Minute * 2000)
}
func isPortInUse(address string) bool {
	conn, err := net.Listen("tcp", address)
	if err != nil {
		// 端口被占用
		return true
	}
	// 端口未被占用，关闭监听
	conn.Close()
	return false
}
func (sv2 *Server2) registerServer2(address string) {
	lis, err1 := net.Listen("tcp", address)
	if err1 != nil {
		fmt.Println(err1)
	}
	server := grpc.NewServer()
	RPC.RegisterServer2Server(server, sv2)
	err2 := server.Serve(lis)
	if err2 != nil {
		fmt.Println(err2)
	}
}

func (sv2 *Server2) TellServer(ctx context.Context, args *RPC.FastArgs2) (*RPC.FastReply2, error) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	t1 := time.Now().UnixMilli()
	for {
		//time.Sleep(time.Millisecond * 1)
		//t1 := time.Now().UnixMilli()
		flg := false

		select {
		case <-sv2.writeMsg:
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

func (sv2 *Server2) FastReturnServer(ctx context.Context, args *RPC.FastArgs2) (*RPC.FastReply2, error) {
	now := time.Now()
	if now.UnixMilli()-args.Times > 1500 {
		return nil, status.Errorf(codes.Unimplemented, "method FastReturn not implemented")
	}
	if sv2.writeMsg == nil {
		sv2.writeMsg = make(chan int32)
	}

	//fmt.Println(args.Give)
	sv2.writeMsg <- args.Give
	t := time.Now().UnixMilli()
	fmt.Println(args.Give, " 这次写入花费的时间", t-args.Times)
	return nil, status.Errorf(codes.Unimplemented, "method FastReturn not implemented")
}
