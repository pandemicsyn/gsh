package main

import (
	"bytes"
	"flag"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/user"
	"path"
	"strings"
	"time"
	"sync/atomic"
)

var (
	userarg      = flag.String("user", "", "optional username to use")
	hostsarg     = flag.String("hosts", "", "comma seperated list of hosts")
	timeoutarg   = flag.Int64("timeout", 90, "timeout in seconds")
	timeout      = time.After(90 * time.Second)
	results      = make(chan []string, 10)
	hostsfilearg = flag.String("g", "", "")
	bufferout    = flag.Bool("buffer", false, "")
)

var errCount uint64

func osUsername() (string, string, error) {
	u, err := user.Current()
	if err != nil {
		return "", "", err
	}
	return u.Username, u.HomeDir, nil
}

type agentAuths struct {
	c     net.Conn
	a     agent.Agent
	auths []ssh.AuthMethod
}

func getAgentAuths() *agentAuths {
	conn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		log.Fatal(err)
	}
	ac := agent.NewClient(conn)

	aa := &agentAuths{
		c:     conn,
		a:     ac,
		auths: []ssh.AuthMethod{ssh.PublicKeysCallback(ac.Signers)},
	}
	return aa
}

func execCmd(cmd, hostname, username string, agent *agentAuths, buffer *bool) []string {
	config := &ssh.ClientConfig{
		User: username,
		Auth: agent.auths,
	}

	if strings.Contains(hostname, "@") {
		config.User = strings.Split(hostname, "@")[0]
		hostname = strings.Split(hostname, "@")[1]
	}
	conn, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", hostname), config)
	if err != nil {
		atomic.AddUint64(&errCount, 1)
		return []string{fmt.Sprintf("%s error: %s", hostname, err)}
	}
	session, err := conn.NewSession()
	if err != nil {
		atomic.AddUint64(&errCount, 1)
		return []string{fmt.Sprintf("%s error: %s", hostname, err)}
	}
	defer session.Close()

	if !*bufferout {
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr
		err = session.Run(cmd)
		if err != nil {
			atomic.AddUint64(&errCount, 1)
			return []string{fmt.Sprintf("%s error: %s", hostname, err)}
		}
		return []string{}
	}
	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out
	err = session.Run(cmd)
	if err != nil {
		atomic.AddUint64(&errCount, 1)
		return []string{fmt.Sprintf("%s error: %s", hostname, err)}
	}

	splitout := strings.Split(strings.TrimSpace(out.String()), "\n")
	resp := make([]string, len(splitout))
	for i, l := range splitout {
		resp[i] = fmt.Sprintf("%s: %s", hostname, l)
	}
	return resp
}

func loadHostsFile(shortname string) []string {
	usr, _ := user.Current()
	file := path.Join(usr.HomeDir, ".gsh", shortname)
	buf, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	hosts := strings.Split(strings.TrimSpace(string(buf)), "\n")
	return hosts
}

func main() {
	flag.Parse()

	errCount = 0

	username, _, err := osUsername()
	if err != nil {
		log.Fatal(err)
	}

	if *hostsarg == "" {
		if *hostsfilearg == "" {
			flag.Usage()
			return
		}

	}
	hosts := strings.Split(*hostsarg, ",")

	if *hostsfilearg != "" {
		hosts = loadHostsFile(*hostsfilearg)
	}

	if len(hosts) == 0 {
		flag.Usage()
		return
	}

	if *userarg != "" {
		username = *userarg
	}

	cmd := flag.Args()

	agent := getAgentAuths()
	defer agent.c.Close()

	for _, hostname := range hosts {
		go func(hostname string) {
			results <- execCmd(strings.Join(cmd, " "), hostname, username, agent, bufferout)
		}(hostname)
	}

	for i := 0; i < len(hosts); i++ {
		select {
		case res := <-results:
			for _, l := range res {
				fmt.Println(l)
			}
		case <-timeout:
			fmt.Println("!! - Timed out - !!")
			return
		}
	}

	if atomic.LoadUint64(&errCount) != 1 {
		os.Exit(1)
	}
}
