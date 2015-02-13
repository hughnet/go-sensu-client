package metrics

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"plugins"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TCP Network stats
//
// gobs of this code lifted from: https://github.com/grahamking/latency/
//
// DESCRIPTION
//  This plugin attempts to determine network latency. It can optionally reboot the device when the specified
// interface is up and we cannot ping.
//
// OUTPUT
//   Graphite plain-text format (name value timestamp\n)
//
// PLATFORMS
//   Linux

const TCP_STATS_NAME = "tcp_metrics"

type TcpStats struct {
	flags            *flag.FlagSet
	networkInterface string
	listenInterface  string
	remoteAddress    string
	localAddress     string
	networkPort      int
	timeout          float64
	workingTimeout   time.Duration
	retryCount       int
	reboot           bool
	rebootStatFile   string
	hostNiceName     string

	failedNetwork bool
}

func init() {
	plugins.Register("tcp_metrics", new(TcpStats))
}

func (tcp *TcpStats) Init(config plugins.PluginConfig) (string, error) {
	tcp.flags = flag.NewFlagSet("tcp-metrics", flag.ContinueOnError)

	tcp.flags.StringVar(&tcp.networkInterface, "test-interface", "", "The Network to test before pinging, defaults to the listen interface")
	tcp.flags.StringVar(&tcp.listenInterface, "i", "", "The network interface to listen on")
	tcp.flags.StringVar(&tcp.remoteAddress, "host", "", "The Network Address to ping")
	tcp.flags.IntVar(&tcp.networkPort, "port", 22, "The Port to SYN (Ping)")
	tcp.flags.Float64Var(&tcp.timeout, "timeout", 10, "Number of seconds to wait for a response")
	tcp.flags.IntVar(&tcp.retryCount, "retry-count", 3, "The number of times to retry before failing")
	tcp.flags.BoolVar(&tcp.reboot, "reboot", false, "If the network is up and ping does not work - reboot")
	tcp.flags.StringVar(&tcp.rebootStatFile, "reboot-stat-file", "", "If specified this file is written to before the reboot action and a system.reboot.tcp counter sent after reboot")

	var err error
	if len(config.Args) > 1 {
		err = tcp.flags.Parse(config.Args[1:])
		if nil != err {
			log.Printf("Failed to parse process check command line: %s", err)
		}
	}

	if "" == tcp.listenInterface {
		return TCP_STATS_NAME, fmt.Errorf("You need to specify an Interface! e.g.: -i eth0")
	}

	if "" == tcp.networkInterface {
		tcp.networkInterface = tcp.listenInterface
	}

	if "" == tcp.remoteAddress {
		return TCP_STATS_NAME, fmt.Errorf("You need to specify a host to ping! e.g.: -host 10.0.0.1")
	}

	tcp.workingTimeout, err = time.ParseDuration(fmt.Sprintf("%0.0fms", tcp.timeout*1000))
	if err != nil {
		log.Println(err)
	}

	log.Printf("Working Duration Timeout: %s", tcp.workingTimeout.String())

	r := regexp.MustCompile("[^0-9a-zA-Z]")
	tcp.hostNiceName = r.ReplaceAllString(tcp.remoteAddress, "_")

	return TCP_STATS_NAME, err
}

func (tcp *TcpStats) Gather(r *plugins.Result) error {
	// measure TCP/IP response
	var rebootCount uint

	stat, err := os.Stat("/sys/class/net/" + tcp.networkInterface)
	if nil != err {
		return fmt.Errorf("Interface %s does not exist.", tcp.networkInterface)
	}

	if !stat.IsDir() {
		return fmt.Errorf("Interface %s does not exist.", tcp.networkInterface)
	}

	// is the network interface up?
	state, err := ioutil.ReadFile("/sys/class/net/" + tcp.networkInterface + "/operstate")
	if nil != err {
		return fmt.Errorf("Unable to determine if interface is up.")
	}
	// cannot ping when the network is down
	if "up" != string(state[0:2]) {
		return fmt.Errorf("Network Interface %s is down", tcp.networkInterface)
	}

	iface, err := interfaceAddress(tcp.listenInterface)
	if err != nil {
		log.Print(err)
		// network is in a failed state?
		tcp.failedNetwork = true
	} else {
		tcp.localAddress = strings.Split(iface.String(), "/")[0]
	}

	// does the remoteAddress look like an IP address?
	remoteIp, err := getRemoteAddress(tcp.remoteAddress)
	if err != nil {
		return err
	}

	// if we are storing our reboots for stat collection
	if "" != tcp.rebootStatFile && !tcp.failedNetwork {
		var recoverCount int
		rebootCount, rebootTime := tcp.getRebootCount()

		recoverCount = 0
		if rebootCount > 0 {
			recoverCount = 1
		} else {
			// make sure we have a current time stamp if we did not reboot
			rebootTime = uint(time.Now().Unix())
		}

		r.AddWithTime(fmt.Sprintf("tcp.reboot-count %d", rebootCount), time.Unix(int64(rebootTime), 0))
		r.Add(fmt.Sprintf("tcp.recovery-count %d", recoverCount))

		// finally set our stats back to 0 (now that we have reported them)
		tcp.setRebootCount(uint(0))
	}

	var counter int
	var worked bool
	var totalLatency time.Duration
	if "" != tcp.localAddress {
		for counter < tcp.retryCount {
			counter++
			latency, errPing := tcp.ping(tcp.localAddress, remoteIp, uint16(tcp.networkPort))
			if errPing == nil {
				totalLatency += latency
				r.Add(fmt.Sprintf("tcp.latency.%s.ms %0.2f", tcp.hostNiceName, float32(totalLatency)/float32(time.Millisecond)))
				r.Add(fmt.Sprintf("tcp.try-count.%s %d", tcp.hostNiceName, counter))
				worked = true
				tcp.failedNetwork = false
				break
			}
			totalLatency += tcp.workingTimeout
			log.Printf("Failed TCP Ping check %d...", counter)
		}
	}

	if !worked && tcp.reboot {
		tcp.failedNetwork = true
		// if we have a file to write to, write a reboot counter
		if "" != tcp.rebootStatFile {
			rebootCount = 1
			tcp.setRebootCount(rebootCount)
			r.Add(fmt.Sprintf("tcp.reboot-count %d", rebootCount))
		}
		log.Println("TCP Check Failed - Rebooting the system in 2 seconds")
		// this gives the system time to flush

		time.AfterFunc(2*time.Second, func() {
			// tcp.performReboot()
		})
	}

	return nil
}

func (tcp *TcpStats) GetStatus() string {
	return ""
}

func (tcp *TcpStats) ShowUsage() {
	tcp.flags.PrintDefaults()
}

func (tcp *TcpStats) getRebootCount() (uint, uint) {
	var count uint
	rebootTime := uint(time.Now().Unix())

	content, err := ioutil.ReadFile(tcp.rebootStatFile)
	if err != nil {
		return 0, rebootTime
	}
	// we have an existing count!
	data := strings.Split(string(content), ",")
	c, err := strconv.ParseUint(data[0], 10, 32)
	if err != nil {
		c = 0
	}

	count = uint(c)
	if len(data) == 2 {
		t, err := strconv.ParseUint(data[1], 10, 32)
		if nil == err {
			rebootTime = uint(t)
		}
	}

	return count, rebootTime
}

func (tcp *TcpStats) setRebootCount(count uint) {
	currentValue, currentTimestamp := tcp.getRebootCount()

	newTimeStamp := time.Now().Unix()

	if currentValue == 1 && count == 1 { // keep the timestamp from the intial failure
		newTimeStamp = int64(currentTimestamp)
	}

	value := []byte(fmt.Sprintf("%d,%d", count, newTimeStamp))
	err := ioutil.WriteFile(tcp.rebootStatFile, value, os.FileMode(0644))
	if err != nil {
		log.Println(err)
	}
}

func (tcp *TcpStats) ping(localAddr, remoteAddr string, port uint16) (time.Duration, error) {
	receiveDuration := make(chan time.Duration)
	receiveError := make(chan error)
	timeoutChannel := make(chan bool)

	// limit ourselves to 10 seconds
	time.AfterFunc(tcp.workingTimeout, func() { timeoutChannel <- true })

	go func() {
		t, err := latency(localAddr, remoteAddr, port)
		if err != nil {
			receiveError <- err
		} else {
			receiveDuration <- t
		}
	}()

	select {
	case d := <-receiveDuration:
		return d, nil
	case e := <-receiveError:
		log.Println(e)
		return 0, e
	case <-timeoutChannel:
		return time.Duration(0), fmt.Errorf("Failed to TCP ping remote host")
	}
}
