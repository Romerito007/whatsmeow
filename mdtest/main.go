// Copyright (c) 2021 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var cli *whatsmeow.Client
var log = waLog.Stdout("Main", true)

func getDevice() *store.Device {
	db, err := sql.Open("sqlite3", "file:mdtest.db?_foreign_keys=on")
	if err != nil {
		log.Errorf("Failed to open mdtest.db: %v", err)
		return nil
	}
	storeContainer := sqlstore.NewWithDB(db, "sqlite3", waLog.Stdout("Database", true))
	err = storeContainer.Upgrade()
	if err != nil {
		log.Errorf("Failed to upgrade database: %v", err)
		return nil
	}
	devices, err := storeContainer.GetAllDevices()
	if err != nil {
		log.Errorf("Failed to get devices from database: %v", err)
		return nil
	}
	if len(devices) == 0 {
		return storeContainer.NewDevice()
	} else {
		return devices[0]
	}
}

func main() {
	waBinary.IndentXML = true

	device := getDevice()
	if device == nil {
		return
	}

	cli = whatsmeow.NewClient(device, waLog.Stdout("Client", true))
	err := cli.Connect()
	if err != nil {
		log.Errorf("Failed to connect: %v", err)
		return
	}
	cli.AddEventHandler(handler)

	c := make(chan os.Signal)
	input := make(chan string)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(input)
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if len(line) > 0 {
				input <- line
			}
		}
	}()
	for {
		select {
		case <-c:
			cli.Disconnect()
			return
		case cmd := <-input:
			args := strings.Fields(cmd)
			cmd = args[0]
			args = args[1:]
			go handleCmd(strings.ToLower(cmd), args)
		}
	}
}

func handleCmd(cmd string, args []string) {
	switch cmd {
	case "reconnect":
		cli.Disconnect()
		err := cli.Connect()
		if err != nil {
			log.Errorf("Failed to connect: %v", err)
			return
		}
	case "appstate":
		names := []appstate.WAPatchName{appstate.WAPatchName(args[0])}
		if args[0] == "all" {
			names = []appstate.WAPatchName{appstate.WAPatchRegular, appstate.WAPatchRegularHigh, appstate.WAPatchRegularLow, appstate.WAPatchCriticalUnblockLow, appstate.WAPatchCriticalBlock}
		}
		resync := len(args) > 1 && args[1] == "resync"
		for _, name := range names {
			err := cli.FetchAppState(name, resync, false)
			if err != nil {
				log.Errorf("Failed to sync app state: %v", err)
			}
		}
	case "checkuser":
		resp, err := cli.IsOnWhatsApp(args)
		fmt.Println(err)
		fmt.Printf("%+v\n", resp)
	case "presence":
		fmt.Println(cli.SendPresence(types.Presence(args[0])))
	case "chatpresence":
		jid, _ := types.ParseJID(args[1])
		fmt.Println(cli.SendChatPresence(types.ChatPresence(args[0]), jid))
	case "getuser":
		var jids []types.JID
		for _, jid := range args {
			jids = append(jids, types.NewJID(jid, types.DefaultUserServer))
		}
		resp, err := cli.GetUserInfo(jids)
		fmt.Println(err)
		fmt.Printf("%+v\n", resp)
	case "getavatar":
		jid := types.NewJID(args[0], types.DefaultUserServer)
		if len(args) > 1 && args[1] == "group" {
			jid.Server = types.GroupServer
			args = args[1:]
		}
		pic, err := cli.GetProfilePictureInfo(jid, len(args) > 1 && args[1] == "preview")
		fmt.Println(err)
		fmt.Printf("%+v\n", pic)
	case "getgroup":
		resp, err := cli.GetGroupInfo(types.NewJID(args[0], types.GroupServer))
		fmt.Println(err)
		fmt.Printf("%+v\n", resp)
	case "send", "gsend":
		msg := &waProto.Message{Conversation: proto.String(strings.Join(args[1:], " "))}
		recipient := types.NewJID(args[0], types.DefaultUserServer)
		if cmd == "gsend" {
			recipient.Server = types.GroupServer
		}
		err := cli.SendMessage(recipient, "", msg)
		fmt.Println("Send message response:", err)
	case "sendimg", "gsendimg":
		data, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Printf("Failed to read %s: %v\n", args[0], err)
			return
		}
		uploaded, err := cli.Upload(context.Background(), data, whatsmeow.MediaImage)
		if err != nil {
			fmt.Println("Failed to upload file:", err)
			return
		}
		msg := &waProto.Message{ImageMessage: &waProto.ImageMessage{
			Caption:       proto.String(strings.Join(args[2:], " ")),
			Url:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String(http.DetectContentType(data)),
			FileEncSha256: uploaded.FileEncSHA256,
			FileSha256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
		}}
		recipient := types.NewJID(args[0], types.DefaultUserServer)
		if cmd == "gsendimg" {
			recipient.Server = types.GroupServer
		}
		err = cli.SendMessage(recipient, "", msg)
		fmt.Println("Send image error:", err)
	}
}

var stopQRs = make(chan struct{})

func handler(rawEvt interface{}) {
	switch evt := rawEvt.(type) {
	case *events.QR:
		go printQRs(evt)
	case *events.PairSuccess:
		select {
		case stopQRs <- struct{}{}:
		default:
		}
	case *events.Message:
		log.Infof("Received message: %+v", evt)
		img := evt.Message.GetImageMessage()
		if img != nil {
			data, err := cli.Download(img)
			if err != nil {
				fmt.Println("Failed to download image:", err)
				//return
			}
			exts, _ := mime.ExtensionsByType(img.GetMimetype())
			path := fmt.Sprintf("%s%s", evt.Info.ID, exts[0])
			err = os.WriteFile(path, data, 0600)
			if err != nil {
				fmt.Println("Failed to save image:", err)
				return
			}
			fmt.Println("Saved image to", path)
		}
	case *events.Receipt:
		log.Infof("Received receipt: %+v", evt)
	case *events.AppState:
		log.Debugf("App state event: %+v / %+v", evt.Index, evt.SyncActionValue)
	}
}

func printQRs(evt *events.QR) {
	for _, qr := range evt.Codes {
		fmt.Println("\033[38;2;255;255;255m\u001B[48;2;0;0;0m")
		qrterminal.GenerateHalfBlock(qr, qrterminal.L, os.Stdout)
		fmt.Println("\033[0m")
		select {
		case <-time.After(evt.Timeout):
		case <-stopQRs:
			return
		}
	}
}
