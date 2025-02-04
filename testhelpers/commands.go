package testhelpers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ma "gx/ipfs/QmNTCey11oxhb1AxDnQBRHtdhap6Ctud872NjAYPYYXPuc/go-multiaddr"
	"gx/ipfs/QmR8BauakNcBa3RbE4nbQu76PDiJgoQgz8AJdhJuiU4TAw/go-cid"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmZcLBXKaFe8ND5YHPkJRAwmhJGrVsi1JqDZNyJ4nRK5Mj/go-multiaddr-net"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/config"
	"github.com/filecoin-project/go-filecoin/types"

	"gx/ipfs/QmPVkJMTeRC6iBByPWdrRkD3BE5UXsj5HPzb4kPqL186mS/testify/assert"
	"gx/ipfs/QmPVkJMTeRC6iBByPWdrRkD3BE5UXsj5HPzb4kPqL186mS/testify/require"
)

const (
	// DefaultDaemonCmdTimeout is the default timeout for executing commands.
	DefaultDaemonCmdTimeout = 1 * time.Minute
)

// Output manages running, inprocess, a filecoin command.
type Output struct {
	lk sync.Mutex
	// Input is the the raw input we got.
	Input string
	// Args is the cleaned up version of the input.
	Args []string
	// Code is the unix style exit code, set after the command exited.
	Code int
	// Error is the error returned from the command, after it exited.
	Error  error
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	stdout []byte
	Stderr io.ReadCloser
	stderr []byte

	test testing.TB
}

// ReadStderr returns a string representation of the stderr output.
func (o *Output) ReadStderr() string {
	o.lk.Lock()
	defer o.lk.Unlock()

	return strings.Trim(string(o.stderr), "\n")
}

// ReadStdout returns a string representation of the stdout output.
func (o *Output) ReadStdout() string {
	o.lk.Lock()
	defer o.lk.Unlock()

	return string(o.stdout)
}

// ReadStdoutTrimNewlines returns a string representation of stdout,
// with trailing line breaks removed.
func (o *Output) ReadStdoutTrimNewlines() string {
	// TODO: handle non unix line breaks
	return strings.Trim(o.ReadStdout(), "\n")
}

// RunSuccessFirstLine executes the given command, asserts success and returns
// the first line of stdout.
func RunSuccessFirstLine(td *TestDaemon, args ...string) string {
	return RunSuccessLines(td, args...)[0]
}

// RunSuccessLines executes the given command, asserts success and returns
// an array of lines of the stdout.
func RunSuccessLines(td *TestDaemon, args ...string) []string {
	output := td.RunSuccess(args...)
	result := output.ReadStdoutTrimNewlines()
	return strings.Split(result, "\n")
}

// TestDaemon is used to manage a Filecoin daemon instance for testing purposes.
type TestDaemon struct {
	cmdAddr          string
	swarmAddr        string
	repoDir          string
	genesisFile      string
	keyFiles         []string
	withMiner        string
	autoSealInterval string
	isRelay          bool

	firstRun bool
	init     bool

	lk     sync.Mutex
	Stdin  io.Writer
	Stdout io.Reader
	Stderr io.Reader

	process        *exec.Cmd
	test           *testing.T
	cmdTimeout     time.Duration
	defaultAddress string
	daemonArgs     []string
}

// RepoDir returns the repo directory of the test daemon.
func (td *TestDaemon) RepoDir() string {
	return td.repoDir
}

// CmdAddr returns the command address of the test daemon.
func (td *TestDaemon) CmdAddr() string {
	return td.cmdAddr
}

// SwarmAddr returns the swarm address of the test daemon.
func (td *TestDaemon) SwarmAddr() string {
	return td.swarmAddr
}

// Run executes the given command against the test daemon.
func (td *TestDaemon) Run(args ...string) *Output {
	td.test.Helper()
	return td.RunWithStdin(nil, args...)
}

// RunWithStdin executes the given command against the test daemon, allowing to control
// stdin of the process.
func (td *TestDaemon) RunWithStdin(stdin io.Reader, args ...string) *Output {
	td.test.Helper()
	bin := MustGetFilecoinBinary()

	ctx, cancel := context.WithTimeout(context.Background(), td.cmdTimeout)
	defer cancel()

	// handle Run("cmd subcmd")
	if len(args) == 1 {
		args = strings.Split(args[0], " ")
	}

	finalArgs := append(args, "--repodir="+td.repoDir, "--cmdapiaddr="+td.cmdAddr)

	td.test.Logf("(%s) run: %q\n", td.swarmAddr, strings.Join(finalArgs, " "))
	cmd := exec.CommandContext(ctx, bin, finalArgs...)

	if stdin != nil {
		cmd.Stdin = stdin
	}

	stderr, err := cmd.StderrPipe()
	require.NoError(td.test, err)

	stdout, err := cmd.StdoutPipe()
	require.NoError(td.test, err)

	require.NoError(td.test, cmd.Start())

	stderrBytes, err := ioutil.ReadAll(stderr)
	require.NoError(td.test, err)

	stdoutBytes, err := ioutil.ReadAll(stdout)
	require.NoError(td.test, err)

	td.test.Logf("stdout\n%s", string(stdoutBytes))
	td.test.Logf("stderr\n%s", string(stderrBytes))

	o := &Output{
		Args:   args,
		Stdout: stdout,
		stdout: stdoutBytes,
		Stderr: stderr,
		stderr: stderrBytes,
		test:   td.test,
	}

	err = cmd.Wait()

	switch err := err.(type) {
	case *exec.ExitError:
		if ctx.Err() == context.DeadlineExceeded {
			o.Error = errors.Wrapf(err, "context deadline exceeded for command: %q", strings.Join(finalArgs, " "))
		}

		// TODO: its non-trivial to get the 'exit code' cross platform...
		o.Code = 1
	default:
		o.Error = err
	case nil:
		// okay
	}

	return o
}

// RunSuccess is like Run, but asserts that the command exited successfully.
func (td *TestDaemon) RunSuccess(args ...string) *Output {
	td.test.Helper()
	return td.Run(args...).AssertSuccess()
}

// AssertSuccess asserts that the output represents a successful execution.
func (o *Output) AssertSuccess() *Output {
	o.test.Helper()
	require.NoError(o.test, o.Error)
	oErr := o.ReadStderr()

	require.Equal(o.test, 0, o.Code, oErr)
	require.NotContains(o.test, oErr, "CRITICAL")
	require.NotContains(o.test, oErr, "ERROR")
	require.NotContains(o.test, oErr, "WARNING")
	require.NotContains(o.test, oErr, "Error:")

	return o

}

// RunFail is like Run, but asserts that the command exited with an error
// matching the passed in error.
func (td *TestDaemon) RunFail(err string, args ...string) *Output {
	td.test.Helper()
	return td.Run(args...).AssertFail(err)
}

// AssertFail asserts that the output represents a failed execution, with the error
// matching the passed in error.
func (o *Output) AssertFail(err string) *Output {
	o.test.Helper()
	require.NoError(o.test, o.Error)
	require.Equal(o.test, 1, o.Code)
	require.Empty(o.test, o.ReadStdout())
	require.Contains(o.test, o.ReadStderr(), err)
	return o
}

// GetID returns the id of the daemon.
func (td *TestDaemon) GetID() string {
	out := td.RunSuccess("id")
	var parsed map[string]interface{}
	require.NoError(td.test, json.Unmarshal([]byte(out.ReadStdout()), &parsed))

	return parsed["ID"].(string)
}

// GetAddresses returns all of the addresses of the daemon.
func (td *TestDaemon) GetAddresses() []string {
	out := td.RunSuccess("id")
	var parsed map[string]interface{}
	require.NoError(td.test, json.Unmarshal([]byte(out.ReadStdout()), &parsed))
	adders := parsed["Addresses"].([]interface{})

	res := make([]string, len(adders))
	for i, addr := range adders {
		res[i] = addr.(string)
	}
	return res
}

// ConnectSuccess connects the daemon to another daemon, asserting that
// the operation was successful.
func (td *TestDaemon) ConnectSuccess(remote *TestDaemon) *Output {
	remoteAddrs := remote.GetAddresses()
	delay := 100 * time.Millisecond

	// Connect the nodes
	// This usually works on the first try, but leaving this here, to ensure we
	// connect and don't fail the test.
	var out *Output
Outer:
	for i := 0; i < 5; i++ {
		for j, remoteAddr := range remoteAddrs {
			out = td.Run("swarm", "connect", remoteAddr)
			if out.Error == nil && out.Code == 0 {
				if i > 0 || j > 0 {
					fmt.Printf("WARNING: swarm connect took %d tries", (i+1)*(j+1))
				}
				break Outer
			}
			time.Sleep(delay)
		}
	}
	assert.Equal(td.test, out.Code, 0, "failed to execute swarm connect")

	localID := td.GetID()
	remoteID := remote.GetID()

	connected1 := false
	for i := 0; i < 10; i++ {
		peers1 := td.RunSuccess("swarm", "peers")

		p1 := peers1.ReadStdout()
		connected1 = strings.Contains(p1, remoteID)
		if connected1 {
			break
		}
		td.test.Log(p1)
		time.Sleep(delay)
	}

	connected2 := false
	for i := 0; i < 10; i++ {
		peers2 := remote.RunSuccess("swarm", "peers")

		p2 := peers2.ReadStdout()
		connected2 = strings.Contains(p2, localID)
		if connected2 {
			break
		}
		td.test.Log(p2)
		time.Sleep(delay)
	}

	require.True(td.test, connected1, "failed to connect p1 -> p2")
	require.True(td.test, connected2, "failed to connect p2 -> p1")

	return out
}

// ReadStdout returns a string representation of the stdout of the daemon.
func (td *TestDaemon) ReadStdout() string {
	td.lk.Lock()
	defer td.lk.Unlock()
	out, err := ioutil.ReadAll(td.Stdout)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// ReadStderr returns a string representation of the stderr of the daemon.
func (td *TestDaemon) ReadStderr() string {
	td.lk.Lock()
	defer td.lk.Unlock()
	out, err := ioutil.ReadAll(td.Stderr)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// Start starts up the daemon.
func (td *TestDaemon) Start() *TestDaemon {
	td.createNewProcess()

	require.NoError(td.test, td.process.Start())

	err := td.WaitForAPI()
	if err != nil {
		stdErr, _ := ioutil.ReadAll(td.Stderr)
		stdOut, _ := ioutil.ReadAll(td.Stdout)
		td.test.Errorf("%s\n%s", stdErr, stdOut)
	}

	require.NoError(td.test, err, "Daemon failed to start")

	// on first startup import key pairs, if defined
	if td.firstRun {
		for _, file := range td.keyFiles {
			td.RunSuccess("wallet", "import", file)
		}
	}

	return td
}

// Stop stops the daemon
func (td *TestDaemon) Stop() *TestDaemon {
	if err := td.process.Process.Signal(syscall.SIGINT); err != nil {
		panic(err)
	}
	if _, err := td.process.Process.Wait(); err != nil {
		panic(err)
	}
	return td
}

// Restart restarts the daemon
func (td *TestDaemon) Restart() *TestDaemon {
	td.Stop()
	td.assertNoLogErrors()
	return td.Start()
}

// Shutdown stops the daemon and deletes the repository.
func (td *TestDaemon) Shutdown() {
	if err := td.process.Process.Signal(syscall.SIGTERM); err != nil {
		td.test.Errorf("Daemon Stderr:\n%s", td.ReadStderr())
		td.test.Fatalf("Failed to kill daemon %s", err)
	}

	if td.repoDir == "" {
		panic("testdaemon had no repodir set")
	}

	_ = os.RemoveAll(td.repoDir)
}

// ShutdownSuccess stops the daemon, asserting that it exited successfully.
func (td *TestDaemon) ShutdownSuccess() {
	err := td.process.Process.Signal(syscall.SIGTERM)
	assert.NoError(td.test, err)

	td.assertNoLogErrors()

	_ = os.RemoveAll(td.repoDir)
}

func (td *TestDaemon) assertNoLogErrors() {
	tdErr := td.ReadStderr()

	// We filter out errors expected from the cleanup associated with SIGTERM
	ExpectedErrors := []string{
		regexp.QuoteMeta("BlockSub.Next(): subscription cancelled by calling sub.Cancel()"),
		regexp.QuoteMeta("MessageSub.Next(): subscription cancelled by calling sub.Cancel()"),
		regexp.QuoteMeta("BlockSub.Next(): context canceled"),
		regexp.QuoteMeta("MessageSub.Next(): context canceled"),
	}

	filteredStdErr := tdErr
	for _, errorMsg := range ExpectedErrors {
		filteredStdErr = regexp.MustCompile("(?m)^.*"+errorMsg+".*$").ReplaceAllString(filteredStdErr, "")
	}

	assert.NotContains(td.test, filteredStdErr, "CRITICAL")
	assert.NotContains(td.test, filteredStdErr, "ERROR")
}

// ShutdownEasy stops the daemon using `SIGINT`.
func (td *TestDaemon) ShutdownEasy() {
	err := td.process.Process.Signal(syscall.SIGINT)
	assert.NoError(td.test, err)
	tdOut := td.ReadStderr()
	assert.NoError(td.test, err, tdOut)

	_ = os.RemoveAll(td.repoDir)
}

// WaitForAPI polls if the API on the daemon is available, and blocks until
// it is.
func (td *TestDaemon) WaitForAPI() error {
	var err error
	for i := 0; i < 100; i++ {
		err = tryAPICheck(td)
		if err == nil {
			return nil
		}
		time.Sleep(time.Millisecond * 100)
	}
	return fmt.Errorf("filecoin node failed to come online in given time period (10 seconds); last err = %s", err)
}

// CreateMinerAddr issues a new message to the network, mines the message
// and returns the address of the new miner
// equivalent to:
//     `go-filecoin miner create --from $TEST_ACCOUNT 100000 20`
func (td *TestDaemon) CreateMinerAddr(peer *TestDaemon, fromAddr string) address.Address {
	require := require.New(td.test)

	var wg sync.WaitGroup
	var minerAddr address.Address
	wg.Add(1)
	go func() {
		miner := td.RunSuccess("miner", "create", "--from", fromAddr, "--gas-price", "0", "--gas-limit", "300", "100", "20")
		addr, err := address.NewFromString(strings.Trim(miner.ReadStdout(), "\n"))
		require.NoError(err)
		require.NotEqual(addr, address.Undef)
		minerAddr = addr
		wg.Done()
	}()
	wg.Wait()

	return minerAddr
}

// MinerSetPrice creates an ask for a CURRENTLY MINING test daemon and waits for it to appears on chain
func (td *TestDaemon) MinerSetPrice(minerAddr string, fromAddr string, price string, expiry string) {
	td.RunSuccess("miner", "set-price", "--from", fromAddr, "--miner", minerAddr, "--gas-price", "0", "--gas-limit", "300", price, expiry)
}

// UpdatePeerID updates a currently mining miner's peer ID
func (td *TestDaemon) UpdatePeerID() {
	require := require.New(td.test)
	assert := assert.New(td.test)

	var idOutput map[string]interface{}
	peerIDJSON := td.RunSuccess("id").ReadStdout()
	err := json.Unmarshal([]byte(peerIDJSON), &idOutput)
	require.NoError(err)
	updateCidStr := td.RunSuccess("miner", "update-peerid", "--gas-price=0", "--gas-limit=300", td.GetMinerAddress().String(), idOutput["ID"].(string)).ReadStdoutTrimNewlines()
	updateCid, err := cid.Parse(updateCidStr)
	require.NoError(err)
	assert.NotNil(updateCid)

	td.WaitForMessageRequireSuccess(updateCid)
}

// WaitForMessageRequireSuccess accepts a message cid and blocks until a message with matching cid is included in a
// block. The receipt is then inspected to ensure that the corresponding message receipt had a 0 exit code.
func (td *TestDaemon) WaitForMessageRequireSuccess(msgCid cid.Cid) *types.MessageReceipt {
	args := []string{"message", "wait", msgCid.String(), "--receipt=true", "--message=false"}
	trim := strings.Trim(td.RunSuccess(args...).ReadStdout(), "\n")
	rcpt := &types.MessageReceipt{}
	require.NoError(td.test, json.Unmarshal([]byte(trim), rcpt))
	require.Equal(td.test, 0, int(rcpt.ExitCode))
	return rcpt
}

// CreateWalletAddr adds a new address to the daemons wallet and
// returns it.
// equivalent to:
//     `go-filecoin wallet addrs new`
func (td *TestDaemon) CreateWalletAddr() string {
	td.test.Helper()
	outNew := td.RunSuccess("wallet", "addrs", "new")
	addr := strings.Trim(outNew.ReadStdout(), "\n")
	require.NotEmpty(td.test, addr)
	return addr
}

// Config is a helper to read out the config of the deamon
func (td *TestDaemon) Config() *config.Config {
	cfg, err := config.ReadFile(filepath.Join(td.repoDir, "config.json"))
	require.NoError(td.test, err)
	return cfg
}

// MineAndPropagate mines a block and ensure the block has propagated to all `peers`
// by comparing the current head block of `td` with the head block of each peer in `peers`
func (td *TestDaemon) MineAndPropagate(wait time.Duration, peers ...*TestDaemon) {
	td.RunSuccess("mining", "once")
	// short circuit
	if peers == nil {
		return
	}
	// ensure all peers have same chain head as `td`
	td.MustHaveChainHeadBy(wait, peers)
}

// MustHaveChainHeadBy ensures all `peers` have the same chain head as `td`, by
// duration `wait`
func (td *TestDaemon) MustHaveChainHeadBy(wait time.Duration, peers []*TestDaemon) {
	// will signal all nodes have completed check
	done := make(chan struct{})
	var wg sync.WaitGroup

	expHeadBlks := td.GetChainHead()
	var expHead types.SortedCidSet
	for _, blk := range expHeadBlks {
		expHead.Add(blk.Cid())
	}

	for _, p := range peers {
		wg.Add(1)
		go func(p *TestDaemon) {
			for {
				actHeadBlks := p.GetChainHead()
				var actHead types.SortedCidSet
				for _, blk := range actHeadBlks {
					actHead.Add(blk.Cid())
				}
				if expHead.Equals(actHead) {
					wg.Done()
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(p)
	}

	go func() {
		wg.Wait()
		done <- struct{}{}
	}()

	select {
	case <-done:
		return
	case <-time.After(wait):
		td.test.Fatal("Timeout waiting for chains to sync")
	}
}

// GetChainHead returns the blocks in the head tipset from `td`
func (td *TestDaemon) GetChainHead() []types.Block {
	out := td.RunSuccess("chain", "ls", "--enc=json")
	bc := td.MustUnmarshalChain(out.ReadStdout())
	return bc[0]
}

// MustUnmarshalChain unmarshals the chain from `input` into a slice of blocks
func (td *TestDaemon) MustUnmarshalChain(input string) [][]types.Block {
	chain := strings.Trim(input, "\n")
	var bs [][]types.Block

	for _, line := range bytes.Split([]byte(chain), []byte{'\n'}) {
		var b []types.Block
		if err := json.Unmarshal(line, &b); err != nil {
			td.test.Fatal(err)
		}
		bs = append(bs, b)
	}

	return bs
}

// MakeMoney mines a block and ensures that the block has been propagated to all peers.
func (td *TestDaemon) MakeMoney(rewards int, peers ...*TestDaemon) {
	for i := 0; i < rewards; i++ {
		td.MineAndPropagate(time.Second*1, peers...)
	}
}

// GetDefaultAddress returns the default sender address for this daemon.
func (td *TestDaemon) GetDefaultAddress() string {
	addrs := td.RunSuccess("wallet", "addrs", "ls")
	return strings.Split(addrs.ReadStdout(), "\n")[0]
}

// GetMinerAddress returns the miner address for this daemon.
func (td *TestDaemon) GetMinerAddress() address.Address {
	return td.Config().Mining.MinerAddress
}

func tryAPICheck(td *TestDaemon) error {
	maddr, err := ma.NewMultiaddr(td.cmdAddr)
	if err != nil {
		return err
	}

	_, host, err := manet.DialArgs(maddr)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s/api/id", host)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}

	out := make(map[string]interface{})
	err = json.NewDecoder(resp.Body).Decode(&out)
	if err != nil {
		return fmt.Errorf("liveness check failed: %s", err)
	}

	_, ok := out["ID"]
	if !ok {
		return fmt.Errorf("liveness check failed: ID field not present in output")
	}

	return nil
}

// SwarmAddr allows setting the `swarmAddr` config option on the daemon.
func SwarmAddr(addr string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.swarmAddr = addr
	}
}

// RepoDir allows setting the `repoDir` config option on the daemon.
func RepoDir(dir string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.repoDir = dir
	}
}

// ShouldInit allows setting the `init` config option on the daemon. If
// set, `go-filecoin init` is run before starting up the daemon.
func ShouldInit(i bool) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.init = i
	}
}

// CmdTimeout allows setting the `cmdTimeout` config option on the daemon.
func CmdTimeout(t time.Duration) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.cmdTimeout = t
	}
}

// KeyFile specifies a key file for this daemon to add to their wallet during init
func KeyFile(kf string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.keyFiles = append(td.keyFiles, kf)
	}
}

// DefaultAddress specifies a key file for this daemon to add to their wallet during init
func DefaultAddress(defaultAddr string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.defaultAddress = defaultAddr
	}
}

// AutoSealInterval specifies an interval for automatically sealing
func AutoSealInterval(autoSealInterval string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.autoSealInterval = autoSealInterval
	}
}

// GenesisFile allows setting the `genesisFile` config option on the daemon.
func GenesisFile(a string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.genesisFile = a
	}
}

// WithMiner allows setting the --with-miner flag on init.
func WithMiner(m string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.withMiner = m
	}
}

// IsRelay starts the daemon with the --is-relay option.
func IsRelay(td *TestDaemon) {
	td.isRelay = true
}

// NewDaemon creates a new `TestDaemon`, using the passed in configuration options.
func NewDaemon(t *testing.T, options ...func(*TestDaemon)) *TestDaemon {
	t.Helper()
	// Ensure we have the actual binary
	filecoinBin := MustGetFilecoinBinary()

	dir, err := ioutil.TempDir("", "go-fil-test")
	if err != nil {
		t.Fatal(err)
	}

	td := &TestDaemon{
		test:        t,
		repoDir:     dir,
		init:        true, // we want to init unless told otherwise
		firstRun:    true,
		cmdTimeout:  DefaultDaemonCmdTimeout,
		genesisFile: GenesisFilePath(), // default file includes all test addresses,
	}

	// configure TestDaemon options
	for _, option := range options {
		option(td)
	}

	repoDirFlag := fmt.Sprintf("--repodir=%s", td.repoDir)

	// build command options
	initopts := []string{repoDirFlag}

	if td.genesisFile != "" {
		initopts = append(initopts, fmt.Sprintf("--genesisfile=%s", td.genesisFile))
	}

	if td.withMiner != "" {
		initopts = append(initopts, fmt.Sprintf("--with-miner=%s", td.withMiner))
	}

	if td.defaultAddress != "" {
		initopts = append(initopts, fmt.Sprintf("--default-address=%s", td.defaultAddress))
	}

	if td.autoSealInterval != "" {
		initopts = append(initopts, fmt.Sprintf("--auto-seal-interval-seconds=%s", td.autoSealInterval))
	}

	if td.init {
		t.Logf("run: go-filecoin init %s", initopts)
		out, err := RunInit(td, initopts...)
		if err != nil {
			t.Log(string(out))
			t.Fatal(err)
		}
	}

	//Ask the kernel for a port to avoid conflicts
	cmdPort, err := GetFreePort()
	if err != nil {
		t.Fatal(err)
	}
	swarmPort, err := GetFreePort()
	if err != nil {
		t.Fatal(err)
	}

	td.cmdAddr = fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", cmdPort)
	td.swarmAddr = fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", swarmPort)

	swarmListenFlag := fmt.Sprintf("--swarmlisten=%s", td.swarmAddr)
	cmdAPIAddrFlag := fmt.Sprintf("--cmdapiaddr=%s", td.cmdAddr)
	blockTimeFlag := fmt.Sprintf("--block-time=%s", BlockTimeTest)

	td.daemonArgs = []string{filecoinBin, "daemon", repoDirFlag, cmdAPIAddrFlag, swarmListenFlag, blockTimeFlag}

	if td.isRelay {
		td.daemonArgs = append(td.daemonArgs, "--is-relay")
	}

	return td
}

// RunInit is the equivalent of executing `go-filecoin init`.
func RunInit(td *TestDaemon, opts ...string) ([]byte, error) {
	filecoinBin := MustGetFilecoinBinary()

	finalArgs := append([]string{"init"}, opts...)
	td.test.Logf("(%s) run: %q\n", td.swarmAddr, strings.Join(finalArgs, " "))

	process := exec.Command(filecoinBin, finalArgs...)
	return process.CombinedOutput()
}

// GenesisFilePath returns the path of the WalletFile
func GenesisFilePath() string {
	return ProjectRoot("/fixtures/genesis.car")
}

// ProjectRoot return the project root joined with any path fragments
func ProjectRoot(paths ...string) string {
	gopath, err := GetGoPath()
	if err != nil {
		panic(err)
	}

	allPaths := append([]string{gopath, "/src/github.com/filecoin-project/go-filecoin"}, paths...)

	return filepath.Join(allPaths...)
}

func (td *TestDaemon) createNewProcess() {
	td.test.Logf("(%s) run: %q\n", td.swarmAddr, strings.Join(td.daemonArgs, " "))

	td.process = exec.Command(td.daemonArgs[0], td.daemonArgs[1:]...)
	// disable REUSEPORT, it creates problems in tests
	td.process.Env = append(os.Environ(), "IPFS_REUSEPORT=false")

	// setup process pipes
	var err error
	td.Stdout, err = td.process.StdoutPipe()
	if err != nil {
		td.test.Fatal(err)
	}
	// uncomment this and comment out the following 4 lines to output daemon stderr to os stderr
	//td.process.Stderr = os.Stderr
	td.Stderr, err = td.process.StderrPipe()
	if err != nil {
		td.test.Fatal(err)
	}
	td.Stdin, err = td.process.StdinPipe()
	if err != nil {
		td.test.Fatal(err)
	}
}
