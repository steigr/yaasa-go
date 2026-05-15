package desk_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/steigr/yaasa-go/desk"
)

func ExampleScan() {
	err := desk.Scan(10*time.Second, func(addr, name string, rssi int16) {
		fmt.Printf("found  %s  rssi=%d  %s\n", addr, rssi, name)
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleConnect() {
	d, err := desk.Connect("AA:BB:CC:DD:EE:FF", 15*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Disconnect()
	fmt.Println("connected:", d.Info.Model)
}

func ExampleDesk_CurrentHeight() {
	d, err := desk.Connect("AA:BB:CC:DD:EE:FF", 15*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Disconnect()

	h, err := d.CurrentHeight(10 * time.Second)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(h)
}

func ExampleDesk_WaitForHeight() {
	d, err := desk.Connect("AA:BB:CC:DD:EE:FF", 15*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Disconnect()

	target := desk.HeightFromMM(720)
	tolerance := desk.HeightFromMM(2)

	err = d.WaitForHeight(context.Background(), target, tolerance, 30*time.Second,
		func(current desk.Height) {
			fmt.Printf("  → %s\r", current)
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\narrived at %s\n", target)
}

func ExampleDesk_WaitForPreset() {
	d, err := desk.Connect("AA:BB:CC:DD:EE:FF", 15*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Disconnect()

	err = d.WaitForPreset(context.Background(), 1, 30*time.Second, 500*time.Millisecond, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("preset 1 reached")
}

func ExampleDesk_FetchSitStandTime() {
	d, err := desk.Connect("AA:BB:CC:DD:EE:FF", 15*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Disconnect()

	s, err := d.FetchSitStandTime(10 * time.Second)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("stand %s  sit %s\n", s.StandDuration(), s.SitDuration())
}

func ExampleDesk_AddHeightListener() {
	d, err := desk.Connect("AA:BB:CC:DD:EE:FF", 15*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Disconnect()

	cancel := d.AddHeightListener(func(h desk.Height) {
		fmt.Println(h)
	})
	defer cancel()

	// Trigger a height notification burst by requesting height limits.
	if err := d.RequestHeightLimits(); err != nil {
		log.Fatal(err)
	}
	time.Sleep(5 * time.Second)
}
