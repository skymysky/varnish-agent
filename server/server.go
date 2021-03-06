// Copyright 2019 tree xie
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/gobuffalo/packr/v2"
	"github.com/vicanso/elton"
	basicauth "github.com/vicanso/elton-basic-auth"
	bodyparser "github.com/vicanso/elton-body-parser"
	compress "github.com/vicanso/elton-compress"
	errorhandler "github.com/vicanso/elton-error-handler"
	etag "github.com/vicanso/elton-etag"
	fresh "github.com/vicanso/elton-fresh"
	recover "github.com/vicanso/elton-recover"
	responder "github.com/vicanso/elton-responder"
	staticServe "github.com/vicanso/elton-static-serve"
	"github.com/vicanso/hes"
	"github.com/vicanso/varnish-agent/agent"
	"github.com/vicanso/varnish-agent/director"
)

var (
	errDirectorNotFound = hes.New("The director is not found")
	errDirectorExists   = hes.New("The director is exists")
)

var (
	box = packr.New("statics", "../web/build")
)

type (
	staticFile struct {
		box *packr.Box
	}
)

func (sf *staticFile) Exists(file string) bool {
	return sf.box.Has(file)
}
func (sf *staticFile) Get(file string) ([]byte, error) {
	return sf.box.Find(file)
}
func (sf *staticFile) Stat(file string) os.FileInfo {
	return nil
}
func (sf *staticFile) NewReader(file string) (io.Reader, error) {
	buf, err := sf.Get(file)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(buf), nil
}

func sendFile(c *elton.Context, file string) (err error) {
	buf, err := box.Find(file)
	if err != nil {
		return
	}
	c.SetContentTypeByExt(file)
	c.BodyBuffer = bytes.NewBuffer(buf)
	return
}

// NewServer create a server
func NewServer(ins *agent.Agent, addr string) {
	d := elton.New()

	agentPrefix := ins.AdminPath
	d.Pre(func(req *http.Request) {
		path := req.URL.Path
		if strings.HasPrefix(path, agentPrefix) {
			path = path[len(agentPrefix):]
			if path == "" {
				path = "/"
			}
			req.URL.Path = path
		}
	})

	// 捕捉panic异常，避免程序崩溃
	d.Use(recover.New())

	d.Use(compress.NewDefault())

	d.Use(fresh.NewDefault())
	d.Use(etag.NewDefault())

	d.Use(responder.NewDefault())
	d.Use(bodyparser.NewDefault())
	d.Use(errorhandler.NewDefault())
	auth := os.Getenv("AUTH")
	authMid := func(c *elton.Context) error {
		return c.Next()
	}
	// 使用 basic auth 认证
	if auth != "" {
		authInfo := strings.Split(auth, ":")
		authMid = basicauth.New(basicauth.Config{
			Validate: func(account, pwd string, c *elton.Context) (bool, error) {
				if account != authInfo[0] {
					return false, nil
				}
				if len(authInfo) == 2 && pwd != authInfo[1] {
					return false, nil
				}
				return true, nil
			},
		})
	}

	getDirector := func(name string) (s director.Directors, index int, err error) {
		s, err = ins.GetDirectors()
		if err != nil {
			return
		}
		index = -1
		for i, item := range s {
			if name == item.Name {
				index = i
			}
		}
		return
	}

	// 静态文件
	sf := &staticFile{
		box: box,
	}
	d.GET("/ping", func(c *elton.Context) error {
		c.Body = "pong"
		return nil
	})
	d.GET("/", func(c *elton.Context) error {
		c.CacheMaxAge("10s")
		return sendFile(c, "index.html")
	})
	d.GET("/static/*file", staticServe.New(sf, staticServe.Config{
		Path: "/static",
		// 客户端缓存一年
		MaxAge: 365 * 24 * 3600,
		// 缓存服务器缓存一个小时
		SMaxAge:             60 * 60,
		DenyQueryString:     true,
		DisableLastModified: true,
	}))

	g := elton.NewGroup("", authMid)

	// 获取所有directors
	g.GET("/directors", func(c *elton.Context) (err error) {
		s, err := ins.GetDirectors()
		if err != nil {
			return
		}
		sort.Sort(s)
		c.Body = map[string]interface{}{
			"directors": s,
		}
		return
	})

	// 获取单个director
	g.GET("/directors/:name", func(c *elton.Context) (err error) {
		s, index, err := getDirector(c.Param("name"))
		if err != nil {
			return
		}
		c.Body = s[index]
		return
	})

	// 添加director
	g.POST("/directors", func(c *elton.Context) (err error) {
		d := &director.Director{}
		err = json.Unmarshal(c.RequestBody, d)
		if err != nil {
			return
		}
		if d.Name == "" {
			err = hes.New("The director's name can't be nil")
			return
		}
		s, index, err := getDirector(d.Name)
		if err != nil {
			return
		}
		if index != -1 {
			err = errDirectorExists
			return
		}
		s = append(s, d)
		err = ins.Save(s)
		if err != nil {
			return
		}
		c.Created(d)
		return
	})

	// 更新 director
	g.PATCH("/directors/:name", func(c *elton.Context) (err error) {
		d := &director.Director{}
		err = json.Unmarshal(c.RequestBody, d)
		if err != nil {
			return
		}
		d.Name = c.Param("name")
		s, index, err := getDirector(d.Name)
		if err != nil {
			return
		}

		if index == -1 {
			err = errDirectorNotFound
			return
		}
		s[index] = d
		err = ins.Save(s)
		if err != nil {
			return
		}
		c.NoContent()
		return
	})

	// 删除director
	g.DELETE("/directors/:name", func(c *elton.Context) (err error) {
		s, index, err := getDirector(c.Param("name"))
		if err != nil {
			return
		}
		if index == -1 {
			c.NoContent()
			return
		}
		s = append(s[:index], s[index+1:]...)
		err = ins.Save(s)
		if err != nil {
			return
		}
		c.NoContent()
		return
	})

	// 获取 vcl 配置
	g.GET("/vcl", func(c *elton.Context) (err error) {
		vcl, err := ins.GetVcl()
		if err != nil {
			return
		}
		c.Body = map[string]string{
			"vcl": vcl,
		}
		return
	})

	g.GET("/config", func(c *elton.Context) (err error) {
		c.Body = ins.VarnishConfig
		return
	})

	d.AddGroup(g)

	err := d.ListenAndServe(addr)
	if err != nil {
		panic(err)
	}
}
