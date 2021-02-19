// Copyright 2020 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/textproto"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/AlecAivazis/survey/v2"
	"github.com/dustin/go-humanize"
	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nuid"
	terminal "golang.org/x/term"
	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/nats-io/jsm.go"

	"github.com/nats-io/jsm.go/natscontext"
)

func selectConsumer(mgr *jsm.Manager, stream string, consumer string, force bool) (string, error) {
	if consumer != "" {
		known, err := mgr.IsKnownConsumer(stream, consumer)
		if err != nil {
			return "", err
		}

		if known {
			return consumer, nil
		}
	}

	if force {
		return "", fmt.Errorf("unknown consumer %q > %q", stream, consumer)
	}

	consumers, err := mgr.ConsumerNames(stream)
	if err != nil {
		return "", err
	}

	switch len(consumers) {
	case 0:
		return "", fmt.Errorf("no Consumers are defined for Stream %s", stream)
	default:
		c := ""

		err = survey.AskOne(&survey.Select{
			Message:  "Select a Consumer",
			Options:  consumers,
			PageSize: selectPageSize(len(consumers)),
		}, &c)
		if err != nil {
			return "", err
		}

		return c, nil
	}
}

func selectStreamTemplate(mgr *jsm.Manager, template string, force bool) (string, error) {
	if template != "" {
		known, err := mgr.IsKnownStreamTemplate(template)
		if err != nil {
			return "", err
		}

		if known {
			return template, nil
		}
	}

	if force {
		return "", fmt.Errorf("unknown template %q", template)
	}

	templates, err := mgr.StreamTemplateNames()
	if err != nil {
		return "", err
	}

	switch len(templates) {
	case 0:
		return "", errors.New("no Streams Templates are defined")
	default:
		s := ""

		err = survey.AskOne(&survey.Select{
			Message:  "Select a Stream Template",
			Options:  templates,
			PageSize: selectPageSize(len(templates)),
		}, &s)
		if err != nil {
			return "", err
		}

		return s, nil
	}
}

func selectStream(mgr *jsm.Manager, stream string, force bool) (string, error) {
	streams, err := mgr.StreamNames(nil)
	if err != nil {
		return "", err
	}

	known := false
	for _, s := range streams {
		if s == stream {
			known = true
			break
		}
	}

	if known {
		return stream, nil
	}

	if force {
		return "", fmt.Errorf("unknown stream %q", stream)
	}

	switch len(streams) {
	case 0:
		return "", errors.New("no Streams are defined")
	default:
		s := ""

		err = survey.AskOne(&survey.Select{
			Message:  "Select a Stream",
			Options:  streams,
			PageSize: selectPageSize(len(streams)),
		}, &s)
		if err != nil {
			return "", err
		}

		return s, nil
	}
}

func printJSON(d interface{}) error {
	j, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}

	fmt.Println(string(j))

	return nil
}
func parseDurationString(dstr string) (dur time.Duration, err error) {
	dstr = strings.TrimSpace(dstr)

	if len(dstr) <= 0 {
		return dur, nil
	}

	ls := len(dstr)
	di := ls - 1
	unit := dstr[di:]

	switch unit {
	case "w", "W":
		val, err := strconv.ParseFloat(dstr[:di], 32)
		if err != nil {
			return dur, err
		}

		dur = time.Duration(val*7*24) * time.Hour

	case "d", "D":
		val, err := strconv.ParseFloat(dstr[:di], 32)
		if err != nil {
			return dur, err
		}

		dur = time.Duration(val*24) * time.Hour
	case "M":
		val, err := strconv.ParseFloat(dstr[:di], 32)
		if err != nil {
			return dur, err
		}

		dur = time.Duration(val*24*30) * time.Hour
	case "Y", "y":
		val, err := strconv.ParseFloat(dstr[:di], 32)
		if err != nil {
			return dur, err
		}

		dur = time.Duration(val*24*365) * time.Hour
	case "s", "S", "m", "h", "H":
		dur, err = time.ParseDuration(dstr)
		if err != nil {
			return dur, err
		}

	default:
		return dur, fmt.Errorf("invalid time unit %s", unit)
	}

	return dur, nil
}

// calculates progress bar width for uiprogress:
//
// if it cant figure out the width, assume 80
// if the width is > 80, set to 80 - long progress bars feel weird
// if the width is too small, set it to 20 and just live with the overflow
//
// this ensures a reasonable progress size, ideally we should switch over
// to a spinner for < 20 rather than cause overflows, but thats for later.
func progressWidth() int {
	w, _, err := terminal.GetSize(int(os.Stdout.Fd()))
	if err != nil || w > 80 {
		w = 80
	}

	if w-20 <= 20 {
		return 20
	}

	return w - 20
}

func selectPageSize(count int) int {
	_, h, err := terminal.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		h = 40
	}

	ps := count
	if ps > h-4 {
		ps = h - 4
	}

	return ps
}

func askConfirmation(prompt string, dflt bool) (bool, error) {
	ans := dflt

	err := survey.AskOne(&survey.Confirm{
		Message: prompt,
		Default: dflt,
	}, &ans)

	return ans, err
}

func askOneBytes(prompt string, dflt string, help string) (int64, error) {
	val := ""
	err := survey.AskOne(&survey.Input{
		Message: prompt,
		Default: dflt,
		Help:    help,
	}, &val, survey.WithValidator(survey.Required))
	if err != nil {
		return 0, err
	}

	if val == "-1" {
		val = "0"
	}

	i, err := humanize.ParseBytes(val)
	if err != nil {
		return 0, err
	}

	return int64(i), nil
}

func askOneInt(prompt string, dflt string, help string) (int64, error) {
	val := ""
	err := survey.AskOne(&survey.Input{
		Message: prompt,
		Default: dflt,
		Help:    help,
	}, &val, survey.WithValidator(survey.Required))
	if err != nil {
		return 0, err
	}

	i, err := strconv.Atoi(val)
	if err != nil {
		return 0, err
	}

	return int64(i), nil
}

func splitString(s string) []string {
	return strings.FieldsFunc(s, func(c rune) bool {
		if unicode.IsSpace(c) {
			return true
		}

		if c == ',' {
			return true
		}

		return false
	})
}

func natsOpts() []nats.Option {
	if config == nil {
		return []nats.Option{}
	}

	opts, err := config.NATSOptions()
	kingpin.FatalIfError(err, "configuration error")

	totalWait := 10 * time.Minute
	reconnectDelay := time.Second

	return append(opts, []nats.Option{
		nats.Name("NATS CLI Version " + version),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(reconnectDelay),
		nats.MaxReconnects(int(totalWait / reconnectDelay)),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				log.Printf("Disconnected due to: %s, will attempt reconnects for %.0fm", err.Error(), totalWait.Minutes())
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("Reconnected [%s]", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, _ *nats.Subscription, err error) {
			url := nc.ConnectedUrl()
			if url == "" {
				log.Printf("Unexpected NATS error: %s", err)
			} else {
				log.Printf("Unexpected NATS error from server %s: %s", url, err)
			}
		}),
	}...)
}

func newNatsConn(servers string, opts ...nats.Option) (*nats.Conn, error) {
	if config == nil {
		if ctxError != nil {
			return nil, ctxError
		}

		err := loadContext()
		if err != nil {
			return nil, err
		}
	}

	if servers == "" {
		servers = config.ServerURL()
	}

	return nats.Connect(servers, opts...)
}

func prepareHelper(servers string, opts ...nats.Option) (*nats.Conn, *jsm.Manager, error) {
	if config == nil {
		if ctxError != nil {
			return nil, nil, ctxError
		}

		err := loadContext()
		if err != nil {
			return nil, nil, err
		}
	}

	nc, err := newNatsConn(servers, opts...)
	if err != nil {
		return nil, nil, err
	}

	jsopts := []jsm.Option{
		jsm.WithAPIPrefix(config.JSAPIPrefix()),
		jsm.WithEventPrefix(config.JSEventPrefix()),
	}

	if os.Getenv("NOVALIDATE") == "" {
		jsopts = append(jsopts, jsm.WithAPIValidation(new(SchemaValidator)))
	}

	if timeout != 0 {
		jsopts = append(jsopts, jsm.WithTimeout(timeout))
	}

	if trace {
		jsopts = append(jsopts, jsm.WithTrace())
	}

	mgr, err := jsm.New(nc, jsopts...)
	if err != nil {
		return nil, nil, err
	}

	return nc, mgr, err
}

func humanizeDuration(d time.Duration) string {
	tsecs := d / time.Second
	tmins := tsecs / 60
	thrs := tmins / 60
	tdays := thrs / 24
	tyrs := tdays / 365

	if tyrs > 0 {
		return fmt.Sprintf("%dy%dd%dh%dm%ds", tyrs, tdays%365, thrs%24, tmins%60, tsecs%60)
	}

	if tdays > 0 {
		return fmt.Sprintf("%dd%dh%dm%ds", tdays, thrs%24, tmins%60, tsecs%60)
	}

	if thrs > 0 {
		return fmt.Sprintf("%dh%dm%ds", thrs, tmins%60, tsecs%60)
	}

	if tmins > 0 {
		return fmt.Sprintf("%dm%ds", tmins, tsecs%60)
	}

	return fmt.Sprintf("%.2fs", d.Seconds())
}

func humanizeTime(t time.Time) string {
	return humanizeDuration(time.Since(t))
}

const (
	hdrLine   = "NATS/1.0\r\n"
	crlf      = "\r\n"
	hdrPreEnd = len(hdrLine) - len(crlf)
)

// decodeHeadersMsg will decode and headers.
func decodeHeadersMsg(data []byte) (http.Header, error) {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(data)))
	if l, err := tp.ReadLine(); err != nil || l != hdrLine[:hdrPreEnd] {
		return nil, fmt.Errorf("could not decode headers")
	}

	mh, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("could not decode headers")
	}

	return http.Header(mh), nil
}

type pubData struct {
	Cnt       int
	Count     int
	Unix      int64
	UnixNano  int64
	TimeStamp string
	Time      string
}

func (p *pubData) ID() string {
	return nuid.Next()
}

func pubReplyBodyTemplate(body string, ctr int) ([]byte, error) {
	now := time.Now()
	funcMap := template.FuncMap{
		"Random":    randomString,
		"Count":     func() int { return ctr },
		"Cnt":       func() int { return ctr },
		"Unix":      func() int64 { return now.Unix() },
		"UnixNano":  func() int64 { return now.UnixNano() },
		"TimeStamp": func() string { return now.Format(time.RFC3339) },
		"Time":      func() string { return now.Format(time.Kitchen) },
	}

	templ, err := template.New("body").Funcs(funcMap).Parse(body)
	if err != nil {
		return []byte(body), err
	}

	var b bytes.Buffer
	err = templ.Execute(&b, &pubData{
		Cnt:       ctr,
		Count:     ctr,
		Unix:      now.Unix(),
		UnixNano:  now.UnixNano(),
		TimeStamp: now.Format(time.RFC3339),
		Time:      now.Format(time.Kitchen),
	})
	if err != nil {
		return []byte(body), err
	}

	return b.Bytes(), nil
}

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
var passwordRunes = append(letterRunes, []rune("@#_-%^&()")...)

func randomPassword(length int) string {
	b := make([]rune, length)
	for i := range b {
		b[i] = passwordRunes[rand.Intn(len(passwordRunes))]
	}

	return string(b)
}

func randomString(shortest uint, longest uint) string {
	if shortest > longest {
		shortest, longest = longest, shortest
	}

	desired := int(shortest)
	if longest-shortest <= 0 {
		desired += rand.Intn(int(longest))
	} else {
		desired += rand.Intn(int(longest - shortest))
	}

	b := make([]rune, desired)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}

	return string(b)
}

func parseStringsToHeader(hdrs []string, seq int, msg *nats.Msg) error {
	for _, hdr := range hdrs {
		parts := strings.SplitN(hdr, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid header %q", hdr)
		}

		val, err := pubReplyBodyTemplate(strings.TrimSpace(parts[1]), seq)
		if err != nil {
			log.Printf("Failed to parse Header template for %s: %s", parts[0], err)
			continue
		}

		msg.Header.Add(strings.TrimSpace(parts[0]), string(val))
	}

	return nil
}

func loadContext() error {
	config, ctxError = natscontext.New(cfgCtx, !skipContexts,
		natscontext.WithServerURL(servers),
		natscontext.WithUser(username),
		natscontext.WithPassword(password),
		natscontext.WithCreds(creds),
		natscontext.WithNKey(nkey),
		natscontext.WithCertificate(tlsCert),
		natscontext.WithKey(tlsKey),
		natscontext.WithCA(tlsCA),
		natscontext.WithJSEventPrefix(jsEventPrefix),
		natscontext.WithJSAPIPrefix(jsApiPrefix),
	)

	return ctxError
}

func prepareConfig(_ *kingpin.ParseContext) (err error) {
	loadContext()

	rand.Seed(time.Now().UnixNano())

	return nil
}

func fileAccessible(f string) (bool, error) {
	stat, err := os.Stat(f)
	if err != nil {
		return false, err
	}

	if stat.IsDir() {
		return false, fmt.Errorf("is a directory")
	}

	file, err := os.Open(f)
	if err != nil {
		return false, err
	}
	file.Close()

	return true, nil
}

func renderCluster(cluster *api.ClusterInfo) string {
	if cluster == nil {
		return ""
	}
	peers := []string{cluster.Leader + "*"}
	for _, r := range cluster.Replicas {
		peers = append(peers, r.Name)
	}

	sort.Strings(peers)

	return strings.Join(peers, ", ")
}
