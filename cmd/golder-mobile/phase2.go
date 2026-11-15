//go:build android && cgo

package main

/*
#include <jni.h>
*/
import "C"

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getan/golder/internal/androiduse/adb"
)

type phase2Result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Peer  string `json:"peer,omitempty"`
}

func phase2JSON(peer string, err error) string {
	if err != nil {
		raw, _ := json.Marshal(phase2Result{OK: false, Error: err.Error()})
		return string(raw)
	}
	raw, _ := json.Marshal(phase2Result{OK: true, Peer: peer})
	return string(raw)
}

func phase2Identity(dir string) (*adb.Identity, error) {
	if dir == "" {
		return nil, fmt.Errorf("phase2 identity directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return adb.LoadOrCreateIdentity(filepath.Join(dir, "adb-identity.json"), "golder-android")
}

//export Java_com_getan_golder_mobile_GolderNative_phase2Pair
func Java_com_getan_golder_mobile_GolderNative_phase2Pair(env *C.JNIEnv, clazz C.jclass, address, code, identityDir C.jstring) C.jstring {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity, err := phase2Identity(goString(env, identityDir))
	if err != nil {
		return javaString(env, phase2JSON("", err))
	}
	peer, err := adb.Pair(ctx, goString(env, address), identity, goString(env, code))
	if err != nil {
		return javaString(env, phase2JSON("", err))
	}
	return javaString(env, phase2JSON(peer.Data, nil))
}

//export Java_com_getan_golder_mobile_GolderNative_phase2Start
func Java_com_getan_golder_mobile_GolderNative_phase2Start(env *C.JNIEnv, clazz C.jclass, address, apkPath, packageName, helperClass, certPin C.jstring, appUID C.jint, nonce, identityDir C.jstring) C.jstring {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity, err := phase2Identity(goString(env, identityDir))
	if err != nil {
		return javaString(env, phase2JSON("", err))
	}
	cert, err := identity.TLSCertificate()
	if err != nil {
		return javaString(env, phase2JSON("", err))
	}
	conn, err := adb.Dial(ctx, goString(env, address), &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		return javaString(env, phase2JSON("", err))
	}
	defer conn.Close()
	err = adb.LaunchHelper(
		conn,
		goString(env, apkPath),
		goString(env, packageName),
		goString(env, helperClass),
		goString(env, certPin),
		int(appUID),
		goString(env, nonce),
	)
	return javaString(env, phase2JSON("", err))
}
