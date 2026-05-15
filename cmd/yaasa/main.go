package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steigr/yaasa-go/desk"
	"github.com/steigr/yaasa-go/internal/ipc"
	"tinygo.org/x/bluetooth"
)

var (
	flagVerbose        bool
	flagConnectTimeout time.Duration
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "yaasa",
		Short: "Control a Yaasa (Jiecang FE60) standing desk over BLE",
		Long: strings.Join([]string{
			"yaasa controls a Yaasa Frame Expert (or compatible Jiecang FE60) desk.",
			"",
			"Recommended workflow:",
			"  1. Put the desk in coupling mode (Kopplungsmodus).",
			"  2. Run any command, e.g. \"yaasa height\". If no daemon is running,",
			"     yaasa scans for the first FE60 desk and starts a background daemon.",
			"  3. Later commands without an address use that default daemon.",
			"",
			"You may still pass an explicit address to target/start a daemon for a specific desk.",
		}, "\n"),
	}
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Log raw BLE packets and command timing")
	root.PersistentFlags().DurationVar(&flagConnectTimeout, "connect-timeout", 15*time.Second, "BLE connection timeout")

	root.AddCommand(
		daemonCmd(),
		scanCmd(),
		infoCmd(),
		heightCmd(),
		statsCmd(),
		upCmd(),
		downCmd(),
		stopCmd(),
		moveCmd(),
		presetCmd(),
		monitorCmd(),
		statusCmd(),
		quitCmd(),
	)
	installVerboseCommandLogging(root)
	return root
}

// ── daemon ────────────────────────────────────────────────────────────────────

func daemonCmd() *cobra.Command {
	var keepAlive time.Duration
	cmd := &cobra.Command{
		Use:   "daemon [address]",
		Short: "Connect to the desk and keep the connection alive",
		Long: `Connect to the desk once and serve subsequent commands over a local socket.

If address is omitted, yaasa uses the remembered default desk or scans for the
first FE60 desk and saves it as the default. Once the daemon is running all
other yaasa commands use it automatically — no need to re-enter coupling mode.

Stop the daemon with Ctrl-C or "yaasa quit".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveAddress(args)
			if err != nil {
				return err
			}
			socketPath := ipc.SocketPath(addr)

			d, err := connectDesk(addr)
			if err != nil {
				return err
			}
			if err := saveDefaultAddress(addr); err != nil {
				d.Disconnect() //nolint:errcheck
				return err
			}
			defer func() {
				d.Disconnect() //nolint:errcheck
				os.Remove(socketPath)
			}()

			srv, err := ipc.Listen(socketPath, d)
			if err != nil {
				return err
			}
			defer srv.Close()

			fmt.Printf("Daemon listening on %s\n", socketPath)
			fmt.Printf("Default desk: %s\n", addr)
			fmt.Println("All yaasa commands will now use this connection automatically.")
			fmt.Println("Stop with Ctrl-C or: yaasa quit")

			// Keep-alive: send a periodic height-limit request so the desk's BLE
			// adapter does not drop the connection due to inactivity.
			go func() {
				t := time.NewTicker(keepAlive)
				defer t.Stop()
				for {
					select {
					case <-t.C:
						d.RequestHeightLimits() //nolint:errcheck
					case <-srv.QuitCh():
						return
					}
				}
			}()

			// Serve in the background; wait for SIGINT/SIGTERM or "quit".
			srvErrCh := make(chan error, 1)
			go func() { srvErrCh <- srv.Serve() }()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			select {
			case <-sigCh:
				fmt.Println("\nShutting down daemon...")
			case <-srv.QuitCh():
				fmt.Println("Quit command received. Shutting down...")
			case err := <-srvErrCh:
				if err != nil {
					return fmt.Errorf("daemon: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&keepAlive, "keep-alive", 5*time.Second,
		"Interval for keep-alive pings to prevent the desk dropping the connection")
	return cmd
}

// ── scan ──────────────────────────────────────────────────────────────────────

func scanCmd() *cobra.Command {
	var scanTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for nearby desks advertising the FE60 service",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Scanning for desks (%.0fs)...\n", scanTimeout.Seconds())
			seen := make(map[string]bool)
			err := desk.Scan(scanTimeout, func(addr bluetooth.Address, rssi int16, name string) {
				key := addr.String()
				if seen[key] {
					return
				}
				seen[key] = true
				label := name
				if label == "" {
					label = "(unknown)"
				}
				fmt.Printf("  %-45s  rssi=%-4d  %s\n", key, rssi, label)
			})
			if len(seen) == 0 {
				fmt.Println("No desks found.")
			}
			return err
		},
	}
	cmd.Flags().DurationVar(&scanTimeout, "scan-timeout", 10*time.Second, "How long to scan")
	return cmd
}

// ── info ──────────────────────────────────────────────────────────────────────

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info [address]",
		Short: "Print device information",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := autoDaemonClient(args)
			if err != nil {
				return err
			}
			defer c.Close()
			resp, err := c.Info()
			if err != nil {
				return err
			}
			printInfoResp(resp)
			return nil
		},
	}
}

func printInfoResp(r *ipc.Response) {
	fmt.Printf("Address       %s\n", r.Address)
	printField("Device Name", r.DeviceName)
	printField("Model", r.Model)
	printField("Manufacturer", r.Manufacturer)
	printField("Serial", r.Serial)
	printField("Firmware Rev", r.FirmwareRev)
	printField("Hardware Rev", r.HardwareRev)
	printField("Software Rev", r.SoftwareRev)
	if r.Notifications {
		fmt.Println("  Notifications  active")
	} else {
		fmt.Fprintln(os.Stderr, "  Notifications  UNAVAILABLE")
	}
}

func printField(label, value string) {
	if value != "" {
		fmt.Printf("  %-14s %s\n", label, value)
	}
}

// ── height ────────────────────────────────────────────────────────────────────

func heightCmd() *cobra.Command {
	var inches bool
	cmd := &cobra.Command{
		Use:   "height [address]",
		Short: "Print the current desk height",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := autoDaemonClient(args)
			if err != nil {
				return err
			}
			defer c.Close()
			resp, err := c.Height()
			if err != nil {
				return err
			}
			h := desk.HeightFromMM(resp.HeightMM)
			printHeight(h, inches)
			return nil
		},
	}
	cmd.Flags().BoolVar(&inches, "inches", false, "Print in inches instead of mm")
	return cmd
}

// ── stats ─────────────────────────────────────────────────────────────────────

func statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats [address]",
		Short: "Show accumulated sit/stand time counters",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := autoDaemonClient(args)
			if err != nil {
				return err
			}
			defer c.Close()
			resp, err := c.Stats()
			if err != nil {
				return err
			}
			stand := formatSecs(resp.StandSecs)
			sit := formatSecs(resp.SitSecs)
			fmt.Printf("Stand  %s\n", stand)
			fmt.Printf("Sit    %s\n", sit)
			return nil
		},
	}
}

func formatSecs(secs int64) string {
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
}

// ── up ────────────────────────────────────────────────────────────────────────

func upCmd() *cobra.Command {
	var dur time.Duration
	var mm, speed float64
	cmd := &cobra.Command{
		Use:   "up [address]",
		Short: "Move the desk up",
		Long: `Move the desk up for a fixed duration or a distance in mm.

  --duration 2s          move up for exactly 2 seconds
  --mm 100               move up ~100 mm  (uses --speed to compute duration)
  --mm 100 --speed 40    same, but assume 40 mm/s instead of the default 50`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			moveDur := moveDuration(dur, mm, speed)
			fmt.Printf("Moving up %s\n", moveLabel(mm, moveDur))
			c, addr, err := autoDaemonClient(args)
			if err != nil {
				return err
			}
			defer c.Close()
			return runInterruptible(addr, func() error {
				_, err := c.Up(int(moveDur.Milliseconds()))
				return err
			})
		},
	}
	addMoveFlags(cmd, &dur, &mm, &speed, "up", 55.1)
	return cmd
}

// ── down ──────────────────────────────────────────────────────────────────────

func downCmd() *cobra.Command {
	var dur time.Duration
	var mm, speed float64
	cmd := &cobra.Command{
		Use:   "down [address]",
		Short: "Move the desk down",
		Long: `Move the desk down for a fixed duration or a distance in mm.

  --duration 2s          move down for exactly 2 seconds
  --mm 100               move down ~100 mm  (uses --speed to compute duration)
  --mm 100 --speed 40    same, but assume 40 mm/s instead of the default 50`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			moveDur := moveDuration(dur, mm, speed)
			fmt.Printf("Moving down %s\n", moveLabel(mm, moveDur))
			c, addr, err := autoDaemonClient(args)
			if err != nil {
				return err
			}
			defer c.Close()
			return runInterruptible(addr, func() error {
				_, err := c.Down(int(moveDur.Milliseconds()))
				return err
			})
		},
	}
	addMoveFlags(cmd, &dur, &mm, &speed, "down", 50.0)
	return cmd
}

// addMoveFlags attaches --duration, --mm, and --speed to a command.
// defaultSpeed should be the empirically measured speed for that direction:
// upward movement is slightly slower than downward (gravity asymmetry).
func addMoveFlags(cmd *cobra.Command, dur *time.Duration, mm, speed *float64, dir string, defaultSpeed float64) {
	cmd.Flags().DurationVar(dur, "duration", 500*time.Millisecond,
		"How long to move "+dir+" (overridden by --mm)")
	cmd.Flags().Float64Var(mm, "mm", 0,
		"Distance to move in mm; computes duration via --speed")
	cmd.Flags().Float64Var(speed, "speed", defaultSpeed,
		fmt.Sprintf("Assumed desk speed in mm/s for --mm (%.0f mm/s is the spec; down is typically faster)", defaultSpeed))
}

// moveDuration returns the effective duration for a move command.
// If mm > 0 it takes priority; otherwise dur is used.
func moveDuration(dur time.Duration, mm, speed float64) time.Duration {
	if mm <= 0 {
		return dur
	}
	if speed <= 0 {
		speed = 50.0
	}
	ms := (mm / speed) * 1000.0
	return time.Duration(ms) * time.Millisecond
}

// moveLabel builds a human-readable description of the movement.
func moveLabel(mm float64, d time.Duration) string {
	if mm > 0 {
		return fmt.Sprintf("~%.0f mm  (%s at %.0f mm/s)", mm, d.Round(time.Millisecond), mm/d.Seconds())
	}
	return fmt.Sprintf("for %s", d)
}

// moveFor pulses the desk in one direction for dur, then stops.
//
// The first pulse is sent immediately so the desk starts moving at t=0.
// Subsequent pulses arrive every 200 ms to keep it moving.  Sending the
// first command before starting the ticker eliminates the ~200 ms cold-start
// gap that would otherwise cause short moves to undershoot by ~10 mm.
func moveFor(d *desk.Desk, dur time.Duration, up bool) error {
	if err := d.Wake(); err != nil {
		return err
	}
	pulse := d.MoveDown
	if up {
		pulse = d.MoveUp
	}
	// First pulse at t=0.
	if err := pulse(); err != nil {
		return err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(dur)
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-ticker.C:
			if err := pulse(); err != nil {
				return err
			}
		}
	}
	return d.Stop()
}

// ── stop ──────────────────────────────────────────────────────────────────────

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [address]",
		Short: "Stop desk movement",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := autoDaemonClient(args)
			if err != nil {
				return err
			}
			defer c.Close()
			_, err = c.Stop()
			return err
		},
	}
}

// ── move ──────────────────────────────────────────────────────────────────────

func moveCmd() *cobra.Command {
	var (
		moveTimeout time.Duration
		tolerance   float64
		inches      bool
	)
	cmd := &cobra.Command{
		Use:   "move [address] <height>",
		Short: "Move desk to an absolute height and wait for arrival",
		Long: `Move the desk to the specified height (mm by default).

Starts and uses a background daemon automatically if needed.

Examples:
	  yaasa move 720
	  yaasa move 7e86dea2-... 720
	  yaasa move 28.3 --inches`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			addrArgs, heightArg := splitOptionalAddressValue(args)
			val, err := strconv.ParseFloat(heightArg, 64)
			if err != nil {
				return fmt.Errorf("invalid height %q: %w", heightArg, err)
			}
			var target desk.Height
			if inches {
				target = desk.HeightFromInches(val)
			} else {
				target = desk.HeightFromMM(val)
			}
			c, addr, err := autoDaemonClient(addrArgs)
			if err != nil {
				return err
			}
			defer c.Close()
			fmt.Printf("Moving to %s (±%.1f mm, timeout %s)...\n",
				target, tolerance, moveTimeout)
			return runInterruptible(addr, func() error {
				if _, err := c.Move(target.MM(), tolerance, moveTimeout.Milliseconds()); err != nil {
					return err
				}
				fmt.Printf("  ✓ arrived at %s\n", target)
				return nil
			})
		},
	}
	cmd.Flags().DurationVar(&moveTimeout, "move-timeout", 30*time.Second, "Maximum time to wait")
	cmd.Flags().Float64Var(&tolerance, "tolerance", 2.0, "Acceptable distance from target in mm")
	cmd.Flags().BoolVar(&inches, "inches", false, "Interpret height as inches")
	return cmd
}

// ── preset ────────────────────────────────────────────────────────────────────

func presetCmd() *cobra.Command {
	var (
		save        bool
		moveTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "preset [address] <1|2>",
		Short: "Go to a stored height preset (or save current height with --save)",
		Long: `Move the desk to a stored preset position and wait until it arrives.

Completion is detected by watching height notifications: the desk streams
height updates while moving; silence for 500 ms means it has stopped.

Examples:
  yaasa preset 1
  yaasa preset 2
  yaasa preset <address> 1 --save   # save current height as preset 1`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			addrArgs, presetArg := splitOptionalAddressValue(args)
			n, err := strconv.Atoi(presetArg)
			if err != nil || n < 1 || n > 2 {
				return fmt.Errorf("preset must be 1 or 2 (uplift-ble documents FE60 move commands only for presets 1 and 2)")
			}
			c, addr, err := autoDaemonClient(addrArgs)
			if err != nil {
				return err
			}
			defer c.Close()
			if save {
				fmt.Printf("Saving current height as preset %d...\n", n)
				_, err = c.Preset(n, true, moveTimeout.Milliseconds())
				return err
			}
			fmt.Printf("Moving to preset %d (timeout %s)...\n", n, moveTimeout)
			return runInterruptible(addr, func() error {
				_, err := c.Preset(n, false, moveTimeout.Milliseconds())
				return err
			})
		},
	}
	cmd.Flags().BoolVar(&save, "save", false, "Save current height as this preset")
	cmd.Flags().DurationVar(&moveTimeout, "timeout", 30*time.Second, "Maximum time to wait for the desk to reach the preset")
	return cmd
}

// ── monitor ───────────────────────────────────────────────────────────────────

func monitorCmd() *cobra.Command {
	var (
		inches       bool
		pollInterval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "monitor [address]",
		Short: "Stream height updates until Ctrl-C (stimulates FE62 notifications with FE61 height-limit requests)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveAddress(args)
			if err != nil {
				return err
			}
			// Monitor needs a direct BLE connection for the notification stream.
			// If a daemon is running, connect directly anyway (the daemon keeps
			// the desk alive so no coupling mode is needed).
			d, err := connectDesk(addr)
			if err != nil {
				return err
			}
			if err := saveDefaultAddress(addr); err != nil {
				d.Disconnect() //nolint:errcheck
				return err
			}
			defer d.Disconnect()

			if pollInterval > 0 {
				fmt.Printf("Monitoring height every %s — Ctrl-C to stop.\n", pollInterval)
			} else {
				fmt.Println("Monitoring height — Ctrl-C to stop.")
			}
			d.AddHeightListener(func(h desk.Height) {
				ts := time.Now().Format("15:04:05.000")
				if inches {
					fmt.Printf("[%s]  %s  (%s)\n", ts, h, h.InchesString())
				} else {
					fmt.Printf("[%s]  %s\n", ts, h)
				}
			})
			if err := d.RequestHeightLimits(); err != nil {
				return err
			}

			var pollC <-chan time.Time
			var pollTicker *time.Ticker
			if pollInterval > 0 {
				pollTicker = time.NewTicker(pollInterval)
				defer pollTicker.Stop()
				pollC = pollTicker.C
			}

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sig)

			for {
				select {
				case <-sig:
					fmt.Println("\nStopping.")
					return nil
				case <-pollC:
					if err := d.RequestHeightLimits(); err != nil {
						return fmt.Errorf("request height limits: %w", err)
					}
				}
			}
		},
	}
	cmd.Flags().BoolVar(&inches, "inches", false, "Show height in inches too")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, "How often to send the FE61 height-limit request stimulus; <=0 sends only the initial request")
	return cmd
}

// ── status ────────────────────────────────────────────────────────────────────

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [address]",
		Short: "Show whether the daemon is running and the desk is connected",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveAddress(args)
			if err != nil {
				return err
			}
			c, err := ipc.Dial(ipc.SocketPath(addr))
			if err != nil {
				fmt.Printf("Daemon:        not running\n")
				fmt.Printf("Address:       %s\n", addr)
				return nil
			}
			defer c.Close()
			resp, err := c.Status()
			if err != nil {
				return err
			}
			fmt.Printf("Daemon:        running\n")
			fmt.Printf("Address:       %s\n", addr)
			fmt.Printf("Connected:     %v\n", resp.Connected)
			notifyStr := "active"
			if !resp.Notifications {
				notifyStr = "UNAVAILABLE (height/move won't work)"
			}
			fmt.Printf("Notifications: %s\n", notifyStr)
			return nil
		},
	}
}

// ── quit ──────────────────────────────────────────────────────────────────────

func quitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "quit [address]",
		Short: "Stop the running daemon",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveAddress(args)
			if err != nil {
				return err
			}
			c, err := ipc.Dial(ipc.SocketPath(addr))
			if err != nil {
				return fmt.Errorf("no daemon running for %s", addr)
			}
			defer c.Close()
			c.Quit() //nolint:errcheck — socket closes before response arrives
			fmt.Printf("Daemon stopped for %s.\n", addr)
			return nil
		},
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

const defaultScanTimeout = 5 * time.Second

func splitOptionalAddressValue(args []string) (addrArgs []string, value string) {
	if len(args) == 1 {
		return nil, args[0]
	}
	return []string{args[0]}, args[1]
}

// looksLikeAddress reports whether s is a BLE hardware address rather than a
// name pattern.  On macOS BLE addresses are CoreBluetooth UUIDs
// (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx, 4 hyphens); on Linux they are
// colon-separated hex MACs (XX:XX:XX:XX:XX:XX, 5 colons).
func looksLikeAddress(s string) bool {
	return strings.Count(s, ":") == 5 || strings.Count(s, "-") == 4
}

func resolveAddress(args []string) (string, error) {
	if len(args) > 0 {
		arg := strings.TrimSpace(args[0])
		if arg == "" {
			return "", fmt.Errorf("address must not be empty")
		}
		if looksLikeAddress(arg) {
			return arg, nil
		}
		// Treat as a name pattern — scan and return the first match.
		return scanByName(arg, defaultScanTimeout)
	}

	if addr, ok := readDefaultAddress(); ok {
		return addr, nil
	}

	addr, err := scanFirstDesk(defaultScanTimeout)
	if err != nil {
		return "", err
	}
	return addr, nil
}

func readDefaultAddress() (string, bool) {
	b, err := os.ReadFile(ipc.DefaultAddressPath())
	if err != nil {
		return "", false
	}
	addr := strings.TrimSpace(string(b))
	return addr, addr != ""
}

func saveDefaultAddress(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("default address must not be empty")
	}
	return os.WriteFile(ipc.DefaultAddressPath(), []byte(addr+"\n"), 0o600)
}

func scanFirstDesk(timeout time.Duration) (string, error) {
	fmt.Fprintf(os.Stderr, "No default desk configured; scanning for FE60 desks (%s)...\n", timeout)
	var first string
	err := desk.Scan(timeout, func(addr bluetooth.Address, rssi int16, name string) {
		if first == "" {
			first = addr.String()
			label := name
			if label == "" {
				label = "(unknown)"
			}
			fmt.Fprintf(os.Stderr, "Using first scanned desk as default: %s  rssi=%d  %s\n", first, rssi, label)
		}
	})
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", fmt.Errorf("no FE60 desks found; pass an address explicitly or put the desk in coupling mode")
	}
	return first, nil
}

// scanByName scans for an FE60 desk whose advertised name contains pattern
// (case-insensitive) and returns its address.  The scan runs for at most
// timeout; the first match is used.
func scanByName(pattern string, timeout time.Duration) (string, error) {
	fmt.Fprintf(os.Stderr, "Scanning for desk with name matching %q (%s)...\n", pattern, timeout)
	lower := strings.ToLower(pattern)
	var found string
	err := desk.Scan(timeout, func(addr bluetooth.Address, rssi int16, name string) {
		if found == "" && strings.Contains(strings.ToLower(name), lower) {
			found = addr.String()
			fmt.Fprintf(os.Stderr, "  Matched: %s  rssi=%d  %s\n", found, rssi, name)
		}
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no FE60 desk found with name matching %q (scanned %s)", pattern, timeout)
	}
	return found, nil
}

func autoDaemonClient(args []string) (*ipc.Client, string, error) {
	addr, err := resolveAddress(args)
	if err != nil {
		return nil, "", err
	}
	c, err := ensureDaemonClient(addr)
	if err != nil {
		return nil, "", err
	}
	if err := saveDefaultAddress(addr); err != nil {
		c.Close() //nolint:errcheck
		return nil, "", err
	}
	return c, addr, nil
}

func ensureDaemonClient(addr string) (*ipc.Client, error) {
	if c, ok := daemonClient(addr); ok {
		return c, nil
	}

	logPath, err := startDetachedDaemon(addr)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "Started background daemon for %s (log: %s)\n", addr, logPath)

	deadline := time.Now().Add(flagConnectTimeout + 5*time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if c, ok := daemonClient(addr); ok {
			return c, nil
		}
		lastErr = fmt.Errorf("daemon socket not ready at %s", ipc.SocketPath(addr))
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon for %s did not become ready before timeout: %w (see %s)", addr, lastErr, logPath)
}

func startDetachedDaemon(addr string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find executable: %w", err)
	}

	args := []string{"--connect-timeout", flagConnectTimeout.String()}
	if flagVerbose {
		args = append(args, "--verbose")
	}
	args = append(args, "daemon", addr)

	logPath := ipc.DaemonLogPath(addr)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start daemon process: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return "", fmt.Errorf("release daemon process: %w", err)
	}
	return logPath, nil
}

func installVerboseCommandLogging(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		wrapVerboseCommandLogging(child)
		installVerboseCommandLogging(child)
	}
}

func wrapVerboseCommandLogging(cmd *cobra.Command) {
	if cmd.RunE != nil {
		origRunE := cmd.RunE
		cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
			if !flagVerbose {
				return origRunE(cmd, args)
			}
			start := time.Now()
			logVerboseCommandStart(cmd, args, start)
			defer func() {
				logVerboseCommandEnd(cmd, time.Now(), time.Since(start), err)
			}()
			return origRunE(cmd, args)
		}
		return
	}

	if cmd.Run != nil {
		origRun := cmd.Run
		cmd.Run = func(cmd *cobra.Command, args []string) {
			if !flagVerbose {
				origRun(cmd, args)
				return
			}
			start := time.Now()
			logVerboseCommandStart(cmd, args, start)
			defer func() {
				logVerboseCommandEnd(cmd, time.Now(), time.Since(start), nil)
			}()
			origRun(cmd, args)
		}
	}
}

func logVerboseCommandStart(cmd *cobra.Command, args []string, t time.Time) {
	fmt.Printf("[unix=%d unix_ms=%d] [cmd start] %s args=%q\n", t.Unix(), t.UnixMilli(), cmd.CommandPath(), args)
}

func logVerboseCommandEnd(cmd *cobra.Command, t time.Time, dur time.Duration, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	fmt.Printf("[unix=%d unix_ms=%d] [cmd end] %s status=%s duration_ms=%d\n", t.Unix(), t.UnixMilli(), cmd.CommandPath(), status, dur.Milliseconds())
}

// runInterruptible runs fn in a goroutine and waits for it to return.
// If SIGINT or SIGTERM arrives first, it opens a fresh connection to the daemon
// and sends a stop command — which preempts whatever operation is running
// server-side — then waits for fn to drain and returns nil so the CLI exits
// cleanly instead of printing a spurious error.
func runInterruptible(addr string, fn func() error) error {
	errCh := make(chan error, 1)
	go func() { errCh <- fn() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		return err
	case <-sigCh:
		// Send stop via a fresh connection so it preempts the running op.
		if sc, err := ipc.Dial(ipc.SocketPath(addr)); err == nil {
			sc.Stop() //nolint:errcheck
			sc.Close()
		}
		<-errCh // drain fn (returns within ≤200 ms once the op is cancelled)
		return nil
	}
}

// daemonClient returns an IPC client if the daemon is running for addr.
func daemonClient(addr string) (*ipc.Client, bool) {
	c, err := ipc.Dial(ipc.SocketPath(addr))
	if err != nil {
		return nil, false
	}
	return c, true
}

// connectDesk establishes a direct BLE connection.
func connectDesk(addr string) (*desk.Desk, error) {
	opts := []desk.ConnectOption{desk.WithVerbose(flagVerbose)}
	fmt.Printf("Connecting to %s (direct BLE)...\n", addr)
	d, err := desk.Connect(addr, flagConnectTimeout, opts...)
	if err != nil {
		return nil, err
	}
	if d.NotifyError() != nil {
		// CCCD write was rejected, but the desk may still send notifications —
		// some Jiecang firmware returns ATT 0x11 yet streams FE62 regardless.
		fmt.Fprintf(os.Stderr, "Connected. (WARNING: FE62 subscription returned error: %v — height may still work)\n", d.NotifyError())
	} else {
		fmt.Println("Connected. (height notifications active)")
	}
	return d, nil
}

func printHeight(h desk.Height, inches bool) {
	if inches {
		fmt.Printf("%s  (%s)\n", h.InchesString(), h)
	} else {
		fmt.Println(h)
	}
}
