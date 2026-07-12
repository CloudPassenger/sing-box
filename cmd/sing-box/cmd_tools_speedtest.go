package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/speedtest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/byteformats"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/spf13/cobra"
)

type speedTestCommandOptions struct {
	skipUpload   bool
	skipDownload bool
	useBytes     bool
	quiet        bool
	dataSize     uint32
	timeout      time.Duration
}

var speedTestFlags speedTestCommandOptions

var commandSpeedTest = &cobra.Command{
	Use:   "speedtest",
	Short: "Test server speed",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		err := doSpeedTest()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	commandSpeedTest.Flags().BoolVar(&speedTestFlags.skipUpload, "skip-upload", false, "skip upload test")
	commandSpeedTest.Flags().BoolVar(&speedTestFlags.skipDownload, "skip-download", false, "skip download test")
	commandSpeedTest.Flags().BoolVar(&speedTestFlags.useBytes, "use-bytes", false, "use bytes per second instead of bits per second")
	commandSpeedTest.Flags().BoolVar(&speedTestFlags.quiet, "quiet", false, "quiet mode")
	commandSpeedTest.Flags().Uint32Var(&speedTestFlags.dataSize, "data-size", math.MaxUint32, "data size for download and upload tests, in bytes")
	commandSpeedTest.Flags().DurationVar(&speedTestFlags.timeout, "timeout", time.Minute, "limit duration")
	commandTools.AddCommand(commandSpeedTest)
}

func doSpeedTest() error {
	instance, err := createPreStartedClient()
	if err != nil {
		return err
	}
	defer instance.Close()
	dialer, err := createDialer(instance, commandToolsFlagOutbound)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(globalCtx, os.Interrupt)
	defer cancel()
	return runSpeedTest(ctx, dialer, speedTestFlags)
}

func runSpeedTest(ctx context.Context, dialer N.Dialer, options speedTestCommandOptions) error {
	if options.skipDownload && options.skipUpload {
		return E.New("no speedtest direction enabled")
	}
	done := make(chan error, 1)
	go func() {
		var testErr error
		if !options.skipDownload {
			log.Info("starting download test...")
			err := downloadTest(ctx, dialer, options)
			if err != nil {
				log.Error("download test failed: ", err)
				testErr = err
			}
		}
		if !options.skipUpload {
			log.Info("starting upload test...")
			err := uploadTest(ctx, dialer, options)
			if err != nil {
				log.Error("upload test failed: ", err)
				testErr = err
			}
		}
		done <- testErr
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func downloadTest(ctx context.Context, dialer N.Dialer, options speedTestCommandOptions) error {
	testCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	started := time.Now()
	conn, err := dialer.DialContext(testCtx, N.NetworkTCP, M.Socksaddr{Fqdn: speedtest.MagicAddress})
	if err != nil {
		return err
	}
	defer conn.Close()
	var downloaded uint32
	err = speedtest.DownloadTest(testCtx, conn, options.dataSize, func(duration time.Duration, transferred uint32, end bool) {
		if end {
			log.Info("download complete: downloaded ", transferred, ", speed: ", formatSpeed(transferred, duration, options.useBytes))
			return
		}
		downloaded += transferred
		if !options.quiet {
			log.Info("downloading: downloaded ", downloaded, ", speed: ", formatSpeed(transferred, duration, options.useBytes), ", progress: ", progress(downloaded, options.dataSize))
		}
	})
	if err != nil {
		if E.IsCanceled(err) {
			if downloaded > 0 {
				log.Info("download incomplete: downloaded ", downloaded, ", speed: ", formatSpeed(downloaded, time.Since(started), options.useBytes))
				return nil
			}
			return err
		}
		return err
	}
	return nil
}

func uploadTest(ctx context.Context, dialer N.Dialer, options speedTestCommandOptions) error {
	testCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	started := time.Now()
	conn, err := dialer.DialContext(testCtx, N.NetworkTCP, M.Socksaddr{Fqdn: speedtest.MagicAddress})
	if err != nil {
		return err
	}
	defer conn.Close()
	var uploaded uint32
	err = speedtest.UploadTest(testCtx, conn, options.dataSize, func(duration time.Duration, transferred uint32, end bool) {
		if end {
			log.Info("upload complete: uploaded ", transferred, ", speed: ", formatSpeed(transferred, duration, options.useBytes))
			return
		}
		uploaded += transferred
		if !options.quiet {
			log.Info("uploading: uploaded ", uploaded, ", speed: ", formatSpeed(transferred, duration, options.useBytes), ", progress: ", progress(uploaded, options.dataSize))
		}
	})
	if err != nil {
		if E.IsCanceled(err) {
			if uploaded > 0 {
				log.Info("upload incomplete: uploaded ", uploaded, ", speed: ", formatSpeed(uploaded, time.Since(started), options.useBytes))
				return nil
			}
			return err
		}
		return err
	}
	return nil
}

// formatSpeed formats the transfer rate implied by transferred bytes over
// duration. By default it reports decimal bits per second; useBytes reports
// decimal bytes per second instead.
func formatSpeed(transferred uint32, duration time.Duration, useBytes bool) string {
	seconds := duration.Seconds()
	if seconds == 0 {
		return "N/A"
	}
	bytesPerSecond := float64(transferred) / seconds
	if useBytes {
		return byteformats.FormatBytes(uint64(bytesPerSecond)) + "/s"
	}
	return formatBitsPerSecond(bytesPerSecond * 8)
}

func formatBitsPerSecond(bitsPerSecond float64) string {
	units := []string{"bps", "kbps", "Mbps", "Gbps", "Tbps"}
	value := bitsPerSecond
	unitIndex := 0
	for value >= 1000 && unitIndex < len(units)-1 {
		value /= 1000
		unitIndex++
	}
	formatted := strconv.FormatFloat(value, 'f', 1, 64)
	formatted = strings.TrimSuffix(formatted, ".0")
	return formatted + " " + units[unitIndex]
}

func progress(now, total uint32) string {
	if total == 0 {
		return "0.00%"
	}
	return fmt.Sprintf("%.2f%%", float64(now)/float64(total)*100)
}
