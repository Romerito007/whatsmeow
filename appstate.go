// Copyright (c) 2021 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package whatsmeow

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// EmitAppStateEventsOnFullSync can be set to true if you want to get app state events emitted
// even when re-syncing the whole state.
var EmitAppStateEventsOnFullSync = false

// FetchAppState fetches updates to the given type of app state. If fullSync is true, the current
// cached state will be removed and all app state patches will be re-fetched from the server.
func (cli *Client) FetchAppState(name appstate.WAPatchName, fullSync, onlyIfNotSynced bool) error {
	cli.appStateSyncLock.Lock()
	defer cli.appStateSyncLock.Unlock()
	if fullSync {
		err := cli.Store.AppState.DeleteAppStateVersion(string(name))
		if err != nil {
			return fmt.Errorf("failed to reset app state %s version: %w", name, err)
		}
	}
	version, hash, err := cli.Store.AppState.GetAppStateVersion(string(name))
	if err != nil {
		return fmt.Errorf("failed to get app state %s version: %w", name, err)
	}
	if version == 0 {
		fullSync = true
	} else if onlyIfNotSynced {
		return nil
	}
	state := appstate.HashState{Version: version, Hash: hash}
	hasMore := true
	for hasMore {
		patches, err := cli.fetchAppStatePatches(name, state.Version)
		if err != nil {
			return fmt.Errorf("failed to fetch app state %s patches: %w", name, err)
		}
		hasMore = patches.HasMorePatches

		mutations, newState, err := cli.appStateProc.DecodePatches(patches, state, true)
		if err != nil {
			return fmt.Errorf("failed to decode app state %s patches: %w", name, err)
		}
		state = newState
		for _, mutation := range mutations {
			cli.dispatchAppState(mutation, !fullSync || EmitAppStateEventsOnFullSync)
		}
	}
	if fullSync {
		cli.dispatchEvent(&events.AppStateSyncComplete{Name: name})
	}
	return nil
}

func (cli *Client) dispatchAppState(mutation appstate.Mutation, dispatchEvts bool) {
	if mutation.Operation != waProto.SyncdMutation_SET {
		return
	}

	if dispatchEvts {
		cli.dispatchEvent(&events.AppState{Index: mutation.Index, SyncActionValue: mutation.Action})
	}

	var jid types.JID
	if len(mutation.Index) > 1 {
		jid, _ = types.ParseJID(mutation.Index[1])
	}
	ts := time.Unix(mutation.Action.GetTimestamp(), 0)

	var storeUpdateError error
	var eventToDispatch interface{}
	switch mutation.Index[0] {
	case "mute":
		act := mutation.Action.GetMuteAction()
		eventToDispatch = &events.Mute{JID: jid, Timestamp: ts, Action: act}
		var mutedUntil time.Time
		if act.GetMuted() {
			mutedUntil = time.Unix(act.GetMuteEndTimestamp(), 0)
		}
		if cli.Store.ChatSettings != nil {
			storeUpdateError = cli.Store.ChatSettings.PutMutedUntil(jid, mutedUntil)
		}
	case "pin_v1":
		act := mutation.Action.GetPinAction()
		eventToDispatch = &events.Pin{JID: jid, Timestamp: ts, Action: act}
		if cli.Store.ChatSettings != nil {
			storeUpdateError = cli.Store.ChatSettings.PutPinned(jid, act.GetPinned())
		}
	case "archive":
		act := mutation.Action.GetArchiveChatAction()
		eventToDispatch = &events.Archive{JID: jid, Timestamp: ts, Action: act}
		if cli.Store.ChatSettings != nil {
			storeUpdateError = cli.Store.ChatSettings.PutArchived(jid, act.GetArchived())
		}
	case "contact":
		act := mutation.Action.GetContactAction()
		eventToDispatch = &events.Contact{JID: jid, Timestamp: ts, Action: act}
		if cli.Store.Contacts != nil {
			storeUpdateError = cli.Store.Contacts.PutContactName(jid, act.GetFirstName(), act.GetFullName())
		}
	case "star":
		if len(mutation.Index) < 5 {
			return
		}
		evt := events.Star{
			ChatJID:   jid,
			MessageID: mutation.Index[2],
			Timestamp: ts,
			Action:    mutation.Action.GetStarAction(),
			IsFromMe:  mutation.Index[3] == "1",
		}
		if mutation.Index[4] != "0" {
			evt.SenderJID, _ = types.ParseJID(mutation.Index[4])
		}
		eventToDispatch = &evt
	case "deleteMessageForMe":
		if len(mutation.Index) < 5 {
			return
		}
		evt := events.DeleteForMe{
			ChatJID:   jid,
			MessageID: mutation.Index[2],
			Timestamp: ts,
			Action:    mutation.Action.GetDeleteMessageForMeAction(),
			IsFromMe:  mutation.Index[3] == "1",
		}
		if mutation.Index[4] != "0" {
			evt.SenderJID, _ = types.ParseJID(mutation.Index[4])
		}
		eventToDispatch = &evt
	case "setting_pushName":
		eventToDispatch = &events.PushNameSetting{Timestamp: ts, Action: mutation.Action.GetPushNameSetting()}
		cli.Store.PushName = mutation.Action.GetPushNameSetting().GetName()
		err := cli.Store.Save()
		if err != nil {
			cli.Log.Errorf("Failed to save device store after updating push name: %v", err)
		}
	case "setting_unarchiveChats":
		eventToDispatch = &events.UnarchiveChatsSetting{Timestamp: ts, Action: mutation.Action.GetUnarchiveChatsSetting()}
	}
	if storeUpdateError != nil {
		cli.Log.Errorf("Failed to update device store after app state mutation: %v", storeUpdateError)
	}
	if dispatchEvts && eventToDispatch != nil {
		cli.dispatchEvent(eventToDispatch)
	}
}

func (cli *Client) downloadExternalAppStateBlob(ref *waProto.ExternalBlobReference) (*waProto.SyncdMutations, error) {
	data, err := cli.Download(ref)
	if err != nil {
		return nil, err
	}
	var mutations waProto.SyncdMutations
	err = proto.Unmarshal(data, &mutations)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal mutation list: %w", err)
	}
	return &mutations, nil
}

func (cli *Client) fetchAppStatePatches(name appstate.WAPatchName, fromVersion uint64) (*appstate.PatchList, error) {
	resp, err := cli.sendIQ(infoQuery{
		Namespace: "w:sync:app:state",
		Type:      "set",
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag: "sync",
			Content: []waBinary.Node{{
				Tag: "collection",
				Attrs: waBinary.Attrs{
					"name":            string(name),
					"version":         fromVersion,
					"return_snapshot": false,
				},
			}},
		}},
	})
	if err != nil {
		return nil, err
	}
	return appstate.ParsePatchList(resp, cli.downloadExternalAppStateBlob)
}
