package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"
)

const (
	defaultRunningPath = "./running"
	netType            = "tcp"
	targetPoll         = time.Millisecond * 100
	defaultSwapPath    = "./swap"
	defaultSwapPoll    = 10
	swapFileMode       = 0666
)

//copies sourcePath to destPath (overwriting) with same permissions as source
func copyFile(sourcePath string, destPath string) (err error) {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return
	}
	defer closeFmt(sourceFile, &err)

	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		return
	}

	destFile, err := os.Create(destPath)
	if err != nil {
		return
	}
	defer closeFmt(destFile, &err)

	if err = os.Chmod(destPath, sourceInfo.Mode()); err != nil {
		if rerr := removeIgnore(destPath); rerr != nil {
			err = fmt.Errorf("%v\n%v", err, rerr)
		}
		return
	}

	if _, err = io.Copy(destFile, sourceFile); err != nil {
		if rerr := removeIgnore(destPath); rerr != nil {
			err = fmt.Errorf("%v\n%v", err, rerr)
		}
	}

	return
}

//close and possibly combine the Close() error into err
func closeFmt(closer io.Closer, err *error) {
	if cerr := closer.Close(); cerr != nil {
		*err = fmt.Errorf("%v\n%v", *err, cerr)
	}
}

//close and possibly print a line into the log package
func closeLog(closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Println(err)
	}
}

//remove path, ignoring a doesnt exist error
func removeIgnore(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

//copies src to dest, closes dest writes and then writes signal to done channel
func copyConn(src *net.TCPConn, dest *net.TCPConn, done *sync.WaitGroup) {
	defer done.Done()
	defer func() {
		if err := dest.CloseWrite(); err != nil {
			log.Println(err)
		}
	}()
	//Closing read unneeded and sometimes causes errors

	if _, err := io.Copy(dest, src); err != nil {
		log.Println(err)
	}
}

//copies connections bidirectionally (closing when finished), then calls Done() on wait group
func handle(conn *net.TCPConn, targetAddr *net.TCPAddr, done *sync.WaitGroup) {
	defer done.Done()
	defer closeLog(conn)

	targetConn, err := net.DialTCP(netType, nil, targetAddr)
	if err != nil {
		log.Println(err)
		return
	}
	defer closeLog(targetConn)

	var copyDone sync.WaitGroup
	copyDone.Add(2)
	go copyConn(conn, targetConn, &copyDone)
	go copyConn(targetConn, conn, &copyDone)
	copyDone.Wait()
}

//signals an already started exec.Cmd with an interrupt and waits forever for its exit
func stopCmd(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return err
	}

	_ = cmd.Wait()
	return nil
}

type configuration struct {
	targetPath  string
	targetArgs  []string
	targetAddr  *net.TCPAddr
	runningPath string
	swapPath    string
	swapPoll    time.Duration
}

func readConfig(configPath string) (configuration, error) {
	//read config bytes
	configBytes, err := ioutil.ReadFile(configPath)
	if err != nil {
		return configuration{}, err
	}

	//decode json
	type jsonConfiguration struct {
		ServerPath  string
		ServerArgs  []string
		ServerPort  int
		RunningPath string
		SwapPath    string
		SwapPoll    int
	}
	var jsonConfig jsonConfiguration
	if err = json.Unmarshal(configBytes, &jsonConfig); err != nil {
		return configuration{}, err
	}

	//set defaults
	if jsonConfig.RunningPath == "" {
		jsonConfig.RunningPath = defaultRunningPath
	}
	if jsonConfig.SwapPath == "" {
		jsonConfig.SwapPath = defaultSwapPath
	}
	if jsonConfig.SwapPoll == 0 {
		jsonConfig.SwapPoll = defaultSwapPoll
	}

	//check target path exists
	if _, err := os.Stat(jsonConfig.ServerPath); err != nil {
		return configuration{}, fmt.Errorf("Need a valid ServerPath")
	}

	if jsonConfig.ServerPort == 0 {
		return configuration{}, fmt.Errorf("Need a valid ServerPort")
	}

	return configuration{
		jsonConfig.ServerPath,
		jsonConfig.ServerArgs,
		&net.TCPAddr{nil, jsonConfig.ServerPort, ""},
		jsonConfig.RunningPath,
		jsonConfig.SwapPath,
		time.Second * time.Duration(jsonConfig.SwapPoll),
	}, nil
}

type swapper struct {
	configPath string
	config     configuration
	server     *server
	shutdown   chan bool
	done       sync.WaitGroup
	target     *exec.Cmd
}

func startSwapper(configPath string, server *server) (*swapper, error) {
	config, err := readConfig(configPath)
	if err != nil {
		return &swapper{}, err
	}

	s := swapper{configPath, config, server, make(chan bool), sync.WaitGroup{}, nil}

	//swap first time
	s.swap()

	s.done.Add(1)
	go s.run()

	return &s, nil
}

func (s *swapper) stop() error {
	close(s.shutdown)
	s.done.Wait()
	if err := removeIgnore(s.config.swapPath); err != nil {
		return err
	}
	if err := removeIgnore(s.config.runningPath); err != nil {
		return err
	}
	return nil
}

func (s *swapper) swap() error {
	log.Println("Swapping")

	s.server.pause()

	if err := stopCmd(s.target); err != nil {
		return err
	}

	//remove old swap and running
	if err := removeIgnore(s.config.swapPath); err != nil {
		return err
	}
	if err := removeIgnore(s.config.runningPath); err != nil {
		return err
	}

	//re-read config from file
	if config, err := readConfig(s.configPath); err != nil {
		return err
	} else {
		s.config = config
	}

	//create swap
	if err := removeIgnore(s.config.swapPath); err != nil {
		return err
	}
	if err := ioutil.WriteFile(s.config.swapPath, []byte{}, swapFileMode); err != nil {
		return err
	}

	//copy target to running
	if err := copyFile(s.config.targetPath, s.config.runningPath); err != nil {
		return err
	}

	//start other and redirect its output to ours
	s.target = exec.Command(s.config.runningPath, s.config.targetArgs...)
	targetOut, err := s.target.StdoutPipe()
	if err != nil {
		return err
	}
	targetErr, err := s.target.StderrPipe()
	if err != nil {
		return err
	}
	go io.Copy(os.Stdout, targetOut)
	go io.Copy(os.Stderr, targetErr)
	if err := s.target.Start(); err != nil {
		return err
	}

	//wait until server responds
	for {
		if _, err := net.DialTCP(netType, nil, s.config.targetAddr); err == nil {
			break
		}

		time.Sleep(targetPoll)
	}

	if err := s.server.unpause(); err != nil {
		return err
	}

	log.Println("Swapped")

	return nil
}

func (s *swapper) run() {
	defer s.done.Done()

	ticker := time.Tick(s.config.swapPoll)
	last := time.Now()
	for {
		select {
		case <-s.shutdown:
			if err := stopCmd(s.target); err != nil {
				log.Panicln(err)
			}
			return
		case <-ticker:
			//get swap modified
			info, err := os.Stat(s.config.swapPath)
			if err != nil {
				log.Println(err)
				continue
			}

			//compare modified to current time
			current := info.ModTime()
			if !current.After(last) {
				continue
			}

			if err := s.swap(); err != nil {
				log.Panicln(err)
			}

			ticker = time.Tick(s.config.swapPoll) //in case poll changed on config reload in swap()
			last = time.Now()                     //swap file is rewritten in swap(), so mod time changes
		}
	}
}

type server struct {
	configPath   string
	config       configuration
	listener     *net.TCPListener
	pauseLock    sync.Mutex
	handlersDone sync.WaitGroup
	done         sync.WaitGroup
}

func startServer(configPath string, listenPort int) (*server, error) {
	config, err := readConfig(configPath)
	if err != nil {
		return &server{}, err
	}

	listener, err := net.ListenTCP(netType, &net.TCPAddr{nil, listenPort, ""})
	if err != nil {
		return &server{}, err
	}

	s := server{configPath, config, listener, sync.Mutex{}, sync.WaitGroup{}, sync.WaitGroup{}}

	s.done.Add(1)
	go s.run()

	return &s, nil
}

func (s *server) stop() error {
	if err := s.listener.SetDeadline(time.Now()); err != nil {
		return err
	}
	s.handlersDone.Wait()
	s.done.Wait()
	if err := s.listener.Close(); err != nil {
		return err
	}

	return nil
}

func (s *server) pause() {
	s.pauseLock.Lock()
	s.handlersDone.Wait()
}

func (s *server) unpause() error {
	//re-read config
	if config, err := readConfig(s.configPath); err != nil {
		return err
	} else {
		s.config = config
	}
	s.pauseLock.Unlock()

	return nil
}

func (s *server) run() {
	defer s.done.Done()

	for {
		conn, err := s.listener.AcceptTCP()
		if err == nil {
			s.pauseLock.Lock()
			s.handlersDone.Add(1)
			go handle(conn, s.config.targetAddr, &s.handlersDone)
			s.pauseLock.Unlock()
			continue
		}

		if opErr, ok := err.(*net.OpError); !ok || !opErr.Timeout() {
			log.Panicln(err)
		}

		return
	}
}

func main() {
	var configPath string
	var listenPort int
	flag.IntVar(&listenPort, "listenPort", 8880, "Listen port")
	flag.StringVar(&configPath, "configPath", "", "Configuration file path")
	flag.Parse()

	server, err := startServer(configPath, listenPort)
	if err != nil {
		log.Panicln(err)
	}
	swapper, err := startSwapper(configPath, server)
	if err != nil {
		log.Panicln(err)
	}

	osSignal := make(chan os.Signal, 1) //must use buffer of 1 since we ask to notify before we start waiting
	signal.Notify(osSignal, os.Interrupt, os.Kill)
	<-osSignal

	log.Println("Stopping")
	if err := server.stop(); err != nil { //stop server first so we don't kill the target process while still serving
		log.Panicln(err)
	}
	if err := swapper.stop(); err != nil {
		log.Panicln(err)
	}
	log.Println("Stopped")
}
