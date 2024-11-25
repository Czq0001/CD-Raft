package Raft

import (
	RPC "KV-Raft/RPC"
	PERSISTER "KV-Raft/persist"
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/grpc"
	"math/rand"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type State int
type IntSlice []int32

const NULL int32 = -1

const (
	Follower State = iota // Follower = 0
	Candidate
	Leader
)

type Op struct {
	Option string
	Key    string
	Value  string
	Id     int32
	Seq    int32
	Times  int64
	Ip     string
}

type Entry struct {
	Term    int32
	Command Op
}

type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int
}

type Raft struct {
	mu      *sync.Mutex
	me      int32
	address string
	members []string //其他成员，包括自己
	role    State
	layer   int

	currentTerm  int32
	votedFor     int32
	electionTime time.Duration
	log          []Entry

	commitIndex int32
	lastApplied int32

	nextIndex  []int32
	matchIndex []int32
	costTime   []int64

	voteCh      chan bool
	appendLogCh chan bool
	killCh      chan bool
	applyCh     chan ApplyMsg

	Ulen int32

	Persist *PERSISTER.Persister
}

func getMe(address string) int32 {
	add := strings.Split(address, ".")        // 192.168.8.4:5000
	add = strings.Split(add[len(add)-1], ":") // 4:5000
	me, err := strconv.Atoi(add[0])
	if err != nil {
		fmt.Println(err)
	}
	return int32(me)
}

// 新指令的index，term，isLeader
func (rf *Raft) Start(command interface{}) (int32, int32, bool) {
	var index int32 = -1
	term := rf.currentTerm
	isLeader := rf.role == Leader
	if isLeader {
		index = rf.getLastLogIndex() + 1
		newEntry := Entry{
			Term:    rf.currentTerm,
			Command: command.(Op), //？

		}
		rf.log = append(rf.log, newEntry)
		//fmt.Printf("Start()----新日志的Index：%d，term：%d，内容：%s, startAppendEntries\n", index, newEntry.Term, newEntry.Command)
		if newEntry.Command.Option == "write" {
			//fmt.Println("这是 -- 2", command.(Op), command.(Op).Key, command.(Op).Value)
			rf.startAppendEntries(1, command.(Op).Times, command.(Op).Ip)
		} else if newEntry.Command.Option == "read" {

		}
	}
	return index, term, isLeader
}

func (rf *Raft) registerServer(address string) {
	//Raft内部的Server端
	//for {
	server := grpc.NewServer()
	RPC.RegisterRaftServer(server, rf)
	lis, err1 := net.Listen("tcp", address)
	if err1 != nil {
		fmt.Println(err1)
	}
	err2 := server.Serve(lis)
	if err2 != nil {
		fmt.Println(err2)
	}
	//}
}

func MakeRaft(address string, members []string, persist *PERSISTER.Persister, mu *sync.Mutex, layer int) *Raft {
	raft := &Raft{}
	raft.address = address
	raft.me = getMe(address)
	raft.members = members
	raft.Persist = persist
	raft.mu = mu
	raft.layer = layer
	n := len(raft.members)
	fmt.Printf("当前节点:%s, rf.me=%d, 所有成员地址：\n", raft.address, raft.me)
	for i := 0; i < n; i++ {
		fmt.Println(raft.members[i])
	}
	raft.init()
	return raft
}

func (rf *Raft) init() {
	rf.role = Follower
	rf.currentTerm = 0
	rf.votedFor = NULL
	rf.log = make([]Entry, 1) // 日志索引从1开始
	rf.commitIndex = 0
	rf.lastApplied = 0
	//rf.costTime = make([]int64, 91000)
	rf.voteCh = make(chan bool, 1)
	rf.appendLogCh = make(chan bool, 1)
	rf.killCh = make(chan bool, 1)
	rf.applyCh = make(chan ApplyMsg, 1)
	rf.mu = &sync.Mutex{}
	heartbeatTime := time.Duration(150) * time.Millisecond

	go func() {
		for {
			select {
			case <-rf.killCh:
				return
			default:
			}
			//electionTime := time.Duration(rand.Intn(350)+500) * time.Millisecond
			state := rf.role
			switch state {
			case Follower, Candidate:
				select {
				case <-rf.voteCh:
				case <-rf.appendLogCh:
				//以上两个Ch表示Follower/Candidate已经在对相应的请求进行了响应，否则从程序运行开始就进行选举超时计时了
				case <-time.After(rf.electionTime):
					fmt.Println("######## time.After(electionTime) #######")
					rf.beCandidate()
				}
			case Leader:
				rf.startAppendEntries(2, 0, "")
				fmt.Printf("--------sleep heartbeat time------\n")
				time.Sleep(heartbeatTime)
			}
		}
	}()

	go rf.registerServer(rf.address)

}

func (rf *Raft) startElection() {
	fmt.Printf("############ 开始选举 me:%d term:%d ############\n", rf.me, rf.currentTerm)
	args := &RPC.RequestVoteArgs{
		Term:         rf.currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: rf.getLastLogIndex(),
		LastLogTerm:  rf.getLastLogTerm(),
	}
	var votes int32 = 1 //自己给自己投的一票
	n := len(rf.members)
	for i := 0; i < n; i++ {
		if rf.role != Candidate {
			fmt.Println("Candidate 角色变更")
			return
		}
		if rf.members[i] == rf.address {
			continue
		}
		go func(idx int) {
			if rf.role != Candidate {
				return
			}
			fmt.Printf("%s --> %s RequestVote RPC\n", rf.address, rf.members[idx])
			ret, reply := rf.sendRequestVote(rf.members[idx], args) //一定要有ret
			if ret {
				if reply.Term > rf.currentTerm { // 此Candidate的term过时
					fmt.Println(rf.address, " 的term过期，转成follower")
					rf.beFollower(reply.Term)
					return
				}
				if rf.role != Candidate || rf.currentTerm != args.Term { // 有其他candidate当选了leader或者不是在最新Term（rf.currentTerm）进行的投票
					return
				}
				if reply.VoteGranted {
					fmt.Printf("%s 获得 %s 的投票\n", rf.address, rf.members[idx])
					atomic.AddInt32(&votes, 1)
				} else {
					fmt.Printf("%s 未获得 %s 的投票\n", rf.address, rf.members[idx])
				}
				if atomic.LoadInt32(&votes) > int32(n/2) {
					rf.beLeader()
					send(rf.voteCh)
				}
			} else {
				fmt.Println("RequestVote返回结果失败")
			}
		}(i)
	}
}

func (rf *Raft) sendRequestVote(address string, args *RPC.RequestVoteArgs) (bool, *RPC.RequestVoteReply) {
	// RequestVote RPC 中的Client端
	conn, err1 := grpc.Dial(address, grpc.WithInsecure())
	if err1 != nil {
		fmt.Println(err1)
	}
	defer func() {
		err2 := conn.Close()
		if err2 != nil {
			fmt.Println(err2)
		}
	}()
	client := RPC.NewRaftClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err3 := client.RequestVote(ctx, args)
	if err3 != nil {
		fmt.Println("接受RequestVote结果失败:", err3)
		return false, reply
	}
	return true, reply
}

func (rf *Raft) RequestVote(ctx context.Context, args *RPC.RequestVoteArgs) (*RPC.RequestVoteReply, error) {
	// 方法实现端
	// 发送者：args-term
	// 接收者：rf-currentTerm
	fmt.Printf("\n·····1····%s 收到投票请求：·········\n", rf.address)
	reply := &RPC.RequestVoteReply{VoteGranted: false}
	reply.Term = rf.currentTerm     //用于candidate更新自己的current
	if rf.currentTerm < args.Term { //旧term时的投票已经无效，现在只关心最新term时的投票情况
		fmt.Printf("接收者的term: %d < 发送者的term: %d，成为follower\n", rf.currentTerm, args.Term)
		rf.beFollower(args.Term) // 清空votedFor
	}
	fmt.Printf("请求者：term：%d  index：%d lastTerm:%d\n", args.Term, args.LastLogIndex, args.LastLogTerm)
	fmt.Printf("接收者：term：%d  index：%d lastTerm:%d vote:%d\n", rf.currentTerm, rf.getLastLogIndex(), rf.getLastLogTerm(), rf.votedFor)
	if args.Term >= rf.currentTerm && (rf.votedFor == NULL || rf.votedFor == args.CandidateId) &&
		(args.LastLogTerm > rf.getLastLogTerm() ||
			(args.LastLogTerm == rf.getLastLogTerm() && args.LastLogIndex >= rf.getLastLogIndex())) {
		reply.VoteGranted = true
		rf.votedFor = args.CandidateId
		rf.role = Follower
		send(rf.voteCh)
	}
	if reply.VoteGranted {
		fmt.Printf("·····2····请求者term:%d···%s投出自己的票·······\n\n", args.Term, rf.address)
	} else {
		fmt.Printf("·····2····请求者term:%d···%s拒绝投票·······\n\n", args.Term, rf.address)
	}

	return reply, nil
}

func (rf *Raft) startAppendEntries(flag int, times int64, ip string) {
	//at1 := time.Now().UnixMilli()
	start := time.Now()
	fmt.Printf("############ 开始日志追加 me:%d term:%d ############\n", rf.me, rf.currentTerm)
	fmt.Println(rf.address+" ", flag)
	n := len(rf.members)
	var successCount int32 // 计数器，用于统计成功返回的线程数量
	var czq int32
	czq = 1
	//var bo int32
	//bo = 0
	var wg sync.WaitGroup
	wg.Add(1)
	var wg2 sync.WaitGroup
	wg2.Add(n / 2)
	var wg3 sync.WaitGroup
	wg3.Add(2)
	for i := 0; i < n; i++ {
		if rf.role != Leader {
			return
		}
		if rf.members[i] == rf.address {
			continue
		}
		go func(idx int, ppp int32) {
			for {
				if rf.nextIndex[idx] > int32(len(rf.log)) {
					rf.nextIndex[idx] = int32(len(rf.log))
				}
				appendEntries := rf.log[rf.nextIndex[idx]:]
				if len(appendEntries) > 0 {
					//fmt.Printf("待追加日志长度appendEntries：%d 信息：term：%d cm:%s\n", len(appendEntries), appendEntries[0].Term, appendEntries[0].Command)
				}
				entries, _ := json.Marshal(appendEntries)
				args := &RPC.AppendEntriesArgs{
					Term:         rf.currentTerm,
					LeaderId:     rf.me,
					PrevLogIndex: rf.getPrevLogIndex(idx),
					PrevLogTerm:  rf.getPrevLogTerm(idx),
					Entries:      entries,
					LeaderCommit: rf.commitIndex,
					TypeOp:       flag == 1,
					Flag:         ppp,
					Times:        times,
					Ip:           ip,
				}
				if len(appendEntries) == 0 {
					fmt.Printf("%s --> %s  HeartBeat RPC\n", rf.address, rf.members[idx])
				} else {
					fmt.Printf("%s --> %s  AppendEntries RPC\n", rf.address, rf.members[idx])
				}
				//time1 := time.Now()
				//time1 := time.Now().UnixMilli()
				ret, reply := rf.sendAppendEntries(rf.members[idx], args)
				//time2 := time.Now().UnixMilli()
				//fmt.Println("------------------------------------------------------", time2-time1, "主leader写入", rf.members[idx], "的时间", "写入的数据是", len(rf.log))
				//fmt.Println(rf.)

				if ret {
					if reply.Term > rf.currentTerm {
						rf.beFollower(reply.Term)
						return
					}
					if rf.role != Leader || reply.Term != rf.currentTerm {
						return
					}
					if reply.Success {
						rf.matchIndex[idx] = args.PrevLogIndex + int32(len(appendEntries))
						rf.nextIndex[idx] = rf.matchIndex[idx] + 1
						if len(appendEntries) == 0 {
							fmt.Printf("向 %s 发送心跳包成功，nextIndex[%s]=%d\n", rf.members[idx], rf.members[idx], rf.nextIndex[idx])
						} else {
							fmt.Printf("向 %s 日志追加成功，nextIndex[%s]=%d\n", rf.members[idx], rf.members[idx], rf.nextIndex[idx])
						}
						rf.updateCommitIndex()
						atomic.AddInt32(&successCount, 1) // 安全地递增成功计数器
						num := atomic.LoadInt32(&successCount)
						if num <= int32(n/2) {
							wg2.Done()
						}
						if num <= 2 {
							wg3.Done()
						}
						if rf.members[idx] == ip+":6001" {
							//bo = 1
							wg.Done()
						}

						if rf.members[idx] == "192.168.1.72:6005" && flag == 1 && rf.layer == 1 && ip != "192.168.1.72" {
							go func() {
								if ip == "192.168.1.62" {
									time.Sleep(time.Millisecond * 40)
								} else if ip == "192.168.1.76" {
									time.Sleep(time.Millisecond * 50)
								}
								conn, error1 := grpc.Dial(strings.Split(ip, ":")[0]+":50001", grpc.WithInsecure())
								if error1 != nil {
									fmt.Println("error1")
								}
								defer func() {
									error2 := conn.Close()
									if error2 != nil {
										fmt.Println()
									}
								}()
								sv2 := RPC.NewServer2Client(conn)
								_, err := sv2.FastReturnServer(context.Background(), &RPC.FastArgs2{Give: 1, Times: time.Now().UnixMilli()})
								if err != nil {
									fmt.Println(err)
								}
							}()
						}
						return
					} else {
						rf.nextIndex[idx]--
						fmt.Printf("日志不匹配，更新后nextIndex[%s]=%d\n", rf.members[idx], rf.nextIndex[idx])
					}
				}
			}
		}(i, czq)
		//czq++
	}
	if rf.layer != 1 || ip == "" {
		wg2.Wait() //绝大多数提交
		cost := time.Since(start)
		//if(rf.address =)
		fmt.Println(len(rf.log)+1, " ,ip :", rf.address, " final quit cost : ", cost, " ms")
	} else if rf.layer == 1 {
		if ip != "192.168.1.72" {
			wg.Wait()
		} else {
			wg3.Wait()
		}
	}

}
func (rf *Raft) updateCommitIndex() { // 只由leader调用
	n := len(rf.matchIndex)
	for i := 0; i < n; i++ {
		fmt.Printf("matchIndex[%s]=%d\n", rf.members[i], rf.matchIndex[i])
	}
	copyMatchIndex := make([]int32, n)
	copy(copyMatchIndex, rf.matchIndex)
	sort.Sort(IntSlice(copyMatchIndex))
	N := copyMatchIndex[n/2] // 过半
	fmt.Printf("过半的commitIndex：%d\n", N)
	if N > rf.commitIndex && N < int32(len(rf.log)) && rf.log[N].Term == rf.currentTerm {
		rf.commitIndex = N
		fmt.Printf("new LeaderCommit：%d\n", rf.commitIndex)
		rf.updateLastApplied()
	}
}

func (rf *Raft) updateLastApplied() { // apply
	fmt.Printf("updateLastApplied()---lastApplied: %d, commitIndex: %d\n", rf.lastApplied, rf.commitIndex)
	for rf.lastApplied < rf.commitIndex { // 0 0
		rf.lastApplied++
		curEntry := rf.log[rf.lastApplied]
		cm := curEntry.Command
		if cm.Option == "write" {
			rf.Persist.Put(cm.Key, cm.Value)
			rf.mu.Lock()
			rf.log[rf.lastApplied].Command.Key = "1"
			rf.log[rf.lastApplied].Command.Value = "1"
			rf.mu.Unlock()
			//fmt.Printf("Write 命令：%s-%s 被apply\n", cm.Key, cm.Value)
		} else if cm.Option == "read" {
			fmt.Printf("Read 命令：key:%s 被apply\n", cm.Key)
		}
		applyMsg := ApplyMsg{
			true,
			curEntry.Command,
			int(rf.lastApplied),
		}
		//rf.applyCh <- applyMsg
		applyRequest(rf.applyCh, applyMsg)
	}
}
func applyRequest(ch chan ApplyMsg, msg ApplyMsg) {
	select {
	case <-ch:
	default:
	}
	ch <- msg
}

func (rf *Raft) sendAppendEntries(address string, args *RPC.AppendEntriesArgs) (bool, *RPC.AppendEntriesReply) {
	start := time.Now()

	if address == "192.168.1.62:6001" {
		time.Sleep(time.Millisecond * 40)
	} else if address == "192.168.1.76:6001" {
		time.Sleep(time.Millisecond * 50)
	}

	conn, err1 := grpc.Dial(address, grpc.WithInsecure())
	if err1 != nil {
		fmt.Println(err1)
	}
	defer func() {
		err2 := conn.Close()
		if err2 != nil {
			fmt.Println(err2)
		}
	}()
	client := RPC.NewRaftClient(conn)
	costGrpc := time.Since(start).Milliseconds()
	if args.Flag == 1 {
		fmt.Println(len(rf.log)+1, " ,this leader : ", rf.address, " grpcCost to", address, " cost:", costGrpc, " ms")
	}
	start2 := time.Now()
	reply, err3 := client.AppendEntries(context.Background(), args)
	costAppend := time.Since(start2).Milliseconds()
	if args.Flag == 1 {
		fmt.Println(len(rf.log)+1, " ,this leader : ", rf.address, " grpcAPPEND to", address, " cost:", costAppend, " ms")
	}

	if err3 != nil {
		fmt.Println(err3)
		return false, reply
	}
	cost := time.Since(start).Milliseconds()
	if args.Flag == 1 {
		fmt.Println(len(rf.log)+1, " ,this leader : ", rf.address, " sendAppendEntries to", address, " cost:", cost, " ms")
	}
	return true, reply
}

func (rf *Raft) AppendEntries(ctx context.Context, args *RPC.AppendEntriesArgs) (*RPC.AppendEntriesReply, error) {
	start := time.Now()

	//t1 := time.Now().UnixMilli()
	// 发送者：args-Term
	// 接收者：rf-currentTerm
	fmt.Printf("\n~~~~~~1~~~~~进入AppendEntries~~~~~~~\n")
	reply := &RPC.AppendEntriesReply{}
	reply.Term = rf.currentTerm
	reply.Success = false
	if rf.currentTerm < args.Term { // 接收者发现自己的term过期了，更新term，转成follower
		fmt.Printf("接收者的term: %d < 发送者的term: %d，成为follower\n", rf.currentTerm, args.Term)
		rf.beFollower(args.Term)
	}
	rfLogLen := int32(len(rf.log))
	fmt.Printf("请求者：term：%d  pIndex：%d prevTerm:%d\n", args.Term, args.PrevLogIndex, args.PrevLogTerm)
	if rfLogLen > args.PrevLogIndex {
		fmt.Printf("接收者：term：%d  loglen：%d prevTerm:%d\n", rf.currentTerm, len(rf.log), rf.log[args.PrevLogIndex].Term)
	} else {
		fmt.Printf("接收者：term：%d  loglen：%d prevTerm:NULL\n", rf.currentTerm, len(rf.log))
	}
	if args.Term < rf.currentTerm || rfLogLen <= args.PrevLogIndex ||
		(args.PrevLogIndex < int32(len(rf.log)) && rfLogLen > args.PrevLogIndex && rf.log[args.PrevLogIndex].Term != args.PrevLogTerm) {
		fmt.Printf("~~~~~~2~~~~~日志冲突~~~~~~~\n\n")
		return reply, nil
	}
	fmt.Printf("~~~~~~2~~~~~日志匹配~~~~~~~\n\n")
	start1 := time.Now()
	var newEntries []Entry
	err := json.Unmarshal(args.Entries, &newEntries)
	if err != nil {
		fmt.Println(err)
	}
	if len(newEntries) > 0 {
		//fmt.Printf("日志匹配，待追加日志长度：%d 第一个内容：term：%d command：%s\n", len(newEntries), newEntries[0].Term, newEntries[0].Command)
	} else {
		fmt.Printf("心跳包，待追加日志长度为0\n")
	}
	rf.log = rf.log[:args.PrevLogIndex+1]
	rf.log = append(rf.log, newEntries[0:]...)
	fmt.Printf("############## 更新后的日志 ###########\n")
	if args.LeaderCommit > rf.commitIndex { // not 0 > 0
		rf.commitIndex = Min(args.LeaderCommit, rf.getLastLogIndex())
		rf.updateLastApplied() //在各自的存储引擎中存储相关的内容，在本此实现中，集群成员共用一个存储引擎，此处可以生省略。
	}
	send(rf.appendLogCh)
	reply.Success = true
	cost1 := time.Since(start1).Milliseconds()
	fmt.Println(len(rf.log)+1, " ,this follower: ", rf.address, "AppendEntriesMatchALL cost: ", cost1, "ms")
	var logs []byte
	if rf.Ulen == 0 && rf.layer == 1 {
		logs, _ = json.Marshal(rf.log[0:int32(len(rf.log))])
	} else if rf.Ulen-1 <= int32(len(rf.log)) && rf.layer == 1 {
		logs, _ = json.Marshal(rf.log[rf.Ulen-1 : int32(len(rf.log))])
	}

	args2 := &RPC.AppendEntriesArgs{
		Entries: logs,
		Term:    int32(len(rf.log)),
	}
	costCommit := time.Since(start).Milliseconds()
	fmt.Println(len(rf.log)+1, " ,this follower: ", rf.address, "AppendEntriesCommit cost: ", costCommit, "ms")
	//这里就得改了，如果收到追加心跳不直接给用户返回，而是等待集群成员返回，然后再返回。
	start2 := time.Now()
	if rf.layer == 1 && len(newEntries) > 0 && int32(len(logs)) > 0 {
		start2 := time.Now()
		_, rp := rf.sendCopyData(args2)
		costCopy := time.Since(start2).Milliseconds()
		fmt.Println(len(rf.log)+1, " ,this cost is too long,because len of son len :", rf.Ulen, "and CopyData len :", int32(len(rf.log))-rf.Ulen, " ,CopyData cost: ", costCopy)
		rf.Ulen = rp.Term

	}
	if rf.address == args.Ip+":6001" && args.Flag == 1 && rf.layer == 1 {
		go rf.retToClient(args)
	}
	costFReturn := time.Since(start2).Milliseconds()
	fmt.Println(len(rf.log)+1, " ,this follower: ", rf.address, " cost: ", costFReturn, "ms")
	if rf.address == "192.168.1.62:6001" {
		time.Sleep(time.Millisecond * 40)
	} else if rf.address == "192.168.1.76:6001" {
		time.Sleep(time.Millisecond * 50)
	}
	cost := time.Since(start).Milliseconds()
	fmt.Println(len(rf.log)+1, " ,this follower: ", rf.address, "AppendEntries cost: ", cost, "ms")
	return reply, nil
}

func send(ch chan bool) {
	select {
	case <-ch:
	default:
	}
	ch <- true
}

func (rf *Raft) beCandidate() {
	rf.role = Candidate
	rf.currentTerm++
	rf.votedFor = rf.me
	rf.electionTime = time.Duration(rand.Intn(350)+5000) * time.Millisecond
	if rf.address == "192.168.1.72:6001" {
		rf.electionTime = time.Duration(rand.Intn(350)+400) * time.Millisecond
	}
	fmt.Println(rf.address, " become Candidate, new Term: ", rf.currentTerm)
	go rf.startElection()
}

func (rf *Raft) beFollower(term int32) {
	rf.role = Follower
	rf.votedFor = NULL
	rf.currentTerm = term
}

func (rf *Raft) beLeader() {
	if rf.role != Candidate {
		return
	}
	rf.role = Leader
	n := len(rf.members)
	rf.nextIndex = make([]int32, n)
	rf.matchIndex = make([]int32, n)
	for i := 0; i < n; i++ {
		rf.nextIndex[i] = rf.getLastLogIndex() + 1
		fmt.Printf("beLeader---NextIndex[%s] = %d\n", rf.members[i], rf.nextIndex[i])
	}
	fmt.Println(rf.address, "---------------become LEADER--------------", rf.currentTerm)

}

func (rf *Raft) getLastLogIndex() int32 {
	// 数组下标 0 1 2 3 4
	// len=5, 最新日志索引Index=4，在数组中的下标也为4
	return int32(len(rf.log) - 1) // empty, =0(最新日志索引)
}

func (rf *Raft) getLastLogTerm() int32 {
	index := rf.getLastLogIndex()
	return rf.log[index].Term
}

func (rf *Raft) getPrevLogIndex(i int) int32 {
	// 每个peer对应的nextIndex会变，其相应的prevLogIndex也会变
	fmt.Printf("NextIndex[%s] = %d, prevLogIndex=%d\n", rf.members[i], rf.nextIndex[i], rf.nextIndex[i]-1)
	return rf.nextIndex[i] - 1 //
}
func (rf *Raft) getPrevLogTerm(i int) int32 {
	prevLogIndex := rf.getPrevLogIndex(i)
	if prevLogIndex >= int32(len(rf.log)) {
		prevLogIndex = int32(len(rf.log)) - 1
	}
	fmt.Printf("########prevLogIndex:%d########\n", prevLogIndex)
	fmt.Printf("########prevLogIndexTerm:%d########\n", rf.log[prevLogIndex].Term)
	return rf.log[prevLogIndex].Term
}

func (rf *Raft) GetState() (int32, bool) {
	term := rf.currentTerm
	isLeader := rf.role == Leader
	return term, isLeader
}

func (s IntSlice) Len() int {
	return len(s)
}

func (s IntSlice) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s IntSlice) Less(i, j int) bool {
	return s[i] < s[j]
}

func Min(a, b int32) int32 {
	if a > b {
		return b
	} else {
		return a
	}
}

func (rf *Raft) sendCopyData(args *RPC.AppendEntriesArgs) (bool, *RPC.AppendEntriesReply) {
	start := time.Now()
	address := strings.Split(rf.address, ":")[0] + ":6002"
	conn, err1 := grpc.Dial(address, grpc.WithInsecure())
	if err1 != nil {
		fmt.Println(err1)
	}
	defer func() {
		err2 := conn.Close()
		if err2 != nil {
			fmt.Println(err2)
		}
	}()
	client := RPC.NewRaftClient(conn)
	costGRPC := time.Since(start).Milliseconds()
	fmt.Println(args.Term+1, "this follower is got,grpc cost: ", costGRPC, ",the len :", len(rf.log))
	start = time.Now()
	reply, err3 := client.CopyData(context.Background(), args)
	costGRPC = time.Since(start).Milliseconds()
	fmt.Println(args.Term+1, "this follower is got,real copy cost: ", costGRPC, ",the len :", len(rf.log))
	if err3 != nil {
		fmt.Println(err3)
		return false, reply
	}
	fmt.Println(reply)
	return true, reply
}

func (rf *Raft) CopyData(ctx context.Context, args *RPC.AppendEntriesArgs) (*RPC.AppendEntriesReply, error) {
	//rf.mu.Lock()
	//defer rf.mu.Unlock()
	start := time.Now()
	var newEntries []Entry
	err := json.Unmarshal(args.Entries, &newEntries)
	cost1 := time.Since(start).Milliseconds()
	fmt.Println(args.Term+1, "this leader get copy-data,unmarshal cost:", cost1, "len of this:", len(rf.log))
	if err != nil {
		fmt.Println(err)
	}
	//rf.mu.Lock()
	start = time.Now()
	rf.log = append(rf.log, newEntries[0:]...)
	cost1 = time.Since(start).Milliseconds()
	fmt.Println(args.Term+1, "this leader run copy-data,cost:", cost1, "len of this:", len(rf.log))
	//fmt.Println("这次追加的数据是: ", args.Entries)
	//rf.mu.Unlock()
	start = time.Now()
	rf.startAppendEntries(1, 0, "")
	cost1 = time.Since(start).Milliseconds()
	fmt.Println(args.Term+1, "this leader after copy-data append  cost:", cost1, "len of this:", len(rf.log))
	return &RPC.AppendEntriesReply{Term: int32(len(rf.log))}, nil
}

func (rf *Raft) retToClient(args *RPC.AppendEntriesArgs) {
	if strings.Split(rf.address, ":")[0] != "192.168.1.72" {

		conn, err1 := grpc.Dial(strings.Split(rf.address, ":")[0]+":50001", grpc.WithInsecure())

		if err1 != nil {
			fmt.Println(err1)
		}
		defer func() {
			err2 := conn.Close()
			if err2 != nil {
				fmt.Println(err2)
			}
		}()
		sv2 := RPC.NewServer2Client(conn)
		sv2.TellServer(context.Background(), &RPC.FastArgs2{Give: 1})
	}
	//if args.PrevLogIndex+1 < 200 {
	//	time.Sleep(time.Second)
	//}
	//time.Sleep(time.Millisecond * 20)
	var newEntries []Entry
	json.Unmarshal(args.Entries, &newEntries)
	//fmt.Println("看这里", args.TypeOp, args.Flag, args.PrevLogIndex+1)

	if args.TypeOp == true && args.Flag == 1 {
		fmt.Println("apply 成功")
		//conn, err1 := grpc.Dial("192.168.12.131:40001", grpc.WithInsecure()) //192.168.12.138：13145 建立连接 -》 192.168.12.131：40001
		conn, err1 := grpc.Dial(args.Ip+":40001", grpc.WithInsecure()) //192.168.12.138：13145 建立连接 -》 192.168.12.131：40001
		if err1 != nil {
			fmt.Println(err1)
		}
		defer func() {
			err2 := conn.Close()
			if err2 != nil {
				fmt.Println(err2)
			}
		}()
		client := RPC.NewClientClient(conn)
		fmt.Printf("go fastturn")
		//lens := len(newEntries) - 1
		i64, _ := strconv.ParseInt("1", 10, 32)
		i32 := int32(i64)
		_, err := client.FastReturn(context.Background(), &RPC.FastArgs{Give: i32, Times: args.Times})
		if err != nil {
			fmt.Println("fastreturen return err")
		}
		fmt.Println("写入", args.PrevLogIndex+1)
	}
	fmt.Println("出appendentries")

}

// func (rf *Raft) FastRead() {
//
// }
func (rf *Raft) FastRead(key string) string {
	for {
		if rf.lastApplied >= rf.commitIndex {
			break
		}
	}
	return rf.Persist.Get(key)
}
