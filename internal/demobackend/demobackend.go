// Package demobackend 演示后端：内存里的虚拟设备模拟器，实现适配器契约的两个端点
// （tools manifest + invoke）。它是"适配器可对接任何符合契约的后端"的实证——
// 没有任何真实 IoT 依赖。
//
// 虚拟设备：
//   - 1 客厅灯（可控：power/brightness）
//   - 2 卧室温湿度计（只读：temperature/humidity）
//
// 工具集（3 个）：list_devices / get_device_overview / control_device，
// 描述文本遵循《工具描述写作规范》（双语、三段式、分页教学、确认指令、语义陷阱）。
package demobackend

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// device 虚拟设备内存模型。
type device struct {
	ID          int64
	Name        string
	Kind        string // "light" / "sensor"
	Online      bool
	Power       bool    // light
	Brightness  int     // light: 1-100
	Temperature float64 // sensor
	Humidity    float64 // sensor
}

// Server 演示后端（httptest 与独立进程两种用法共用）。
type Server struct {
	mu    sync.Mutex
	devs  map[int64]*device
	token string // 上游 Bearer 凭据（适配器侧配置同一值）
	mux   *http.ServeMux
}

// New 构建演示后端。token 为上游凭据（空串则 invoke 免鉴权，仅本机演示）。
func New(token string) *Server {
	s := &Server{
		devs: map[int64]*device{
			1: {ID: 1, Name: "living-room-light", Kind: "light", Online: true, Power: false, Brightness: 80},
			2: {ID: 2, Name: "bedroom-thermometer", Kind: "sensor", Online: true, Temperature: 24.5, Humidity: 48},
		},
		token: token,
		mux:   http.NewServeMux(),
	}
	// 端点名为既有契约（manifest 文件名含历史命名；规范定稿后迁移中立命名）。
	s.mux.HandleFunc("GET /v1/tools/openai.json", s.manifest)
	s.mux.HandleFunc("POST /v1/tools/invoke", s.invoke)
	return s
}

// ServeHTTP 实现 http.Handler。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ---------- manifest ----------

// manifest 渲染 FC 格式工具清单（附 annotations 扩展字段——适配器会透传）。
// 描述文本按《工具描述写作规范》撰写：双语、三段式、分页教学、确认指令、自声明。
func (s *Server) manifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name": "list_devices",
				"description": "Lists devices on the demo platform page by page. Use when the user asks what devices exist, to find a device by name (q), or to discover device IDs for get_device_overview / control_device. Parameters: q fuzzy-searches the device name; limit is the page size (1-100, default 20); paginate by passing the previous response's next_cursor until it is empty. Read-only and idempotent." +
					"\n\n分页列出演示平台上的设备。什么时候用：用户问“有哪些设备”、要按名称找某台设备（q）、或需要拿到设备 id 供 get_device_overview / control_device 使用。参数：q 按设备名称模糊搜索；limit 每页条数（1-100，默认 20）；翻页带上一次返回的 next_cursor，直到它为空串。只读、幂等。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"q":      map[string]any{"type": "string", "description": "按名称模糊搜索 / Fuzzy search over device name"},
						"cursor": map[string]any{"type": "string", "description": "分页游标（上一页响应的 next_cursor）/ Pagination cursor from previous next_cursor"},
						"limit":  map[string]any{"type": "integer", "default": 20, "minimum": 1, "maximum": 100, "description": "每页条数（1-100，默认 20）/ Page size (1-100, default 20)"},
					},
				},
				"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name": "get_device_overview",
				"description": "Returns the full picture of one device in a single call: its profile (name, kind) plus its live state (online flag; for a light its power/brightness, for a sensor its temperature/humidity). Prefer this when inspecting a device or answering how a device is doing. Fails with a not_found error when the device does not exist. Read-only and idempotent." +
					"\n\n一次调用返回单台设备的全貌：档案（名称、类型）加实时状态（在线标志；灯含开关/亮度，温湿度计含温度/湿度）。排查设备或回答“这台设备怎么样”时优先用它。设备不存在返回 not_found 错误。只读、幂等。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "integer", "format": "int64", "description": "设备 ID（必填；可用 list_devices 按名称查到）/ Device ID (required; discover via list_devices by name)"},
					},
					"required": []string{"id"},
				},
				"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name": "control_device",
				"description": "Sends a control command to one real (demo) device, e.g. {\"power\":1} or {\"brightness\":60}; keys are command/property names, unknown keys are dropped. Use this only when the user has asked to change a device's state; to inspect a device, use get_device_overview instead. WARNING: this changes the state of a device — BEFORE calling, always tell the user the target device and the exact command, and call only after explicit approval; never decide to execute on your own. Only the light (id discoverable via list_devices) accepts commands; sensors reject with invalid_argument. Not idempotent." +
					"\n\n向一台（演示）设备下发控制命令，如 {\"power\":1} 或 {\"brightness\":60}（键为命令/属性名，非法键被忽略）。仅当用户要求改变设备状态时使用本工具；查看设备状况请改用 get_device_overview。⚠️ 这会改变设备状态：调用前必须先向用户说明目标设备与确切命令内容并获得明确同意，不得自行决定执行。只有灯接受命令（id 可用 list_devices 查到）；温湿度计会返回 invalid_argument。非幂等。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "integer", "format": "int64", "description": "目标设备 ID（必填；可用 list_devices 查到）/ Target device ID (required; discover via list_devices)"},
						"command": map[string]any{"type": "object", "additionalProperties": true, "description": "命令内容（必填）：键=命令/属性名（power: 0/1，brightness: 1-100），值=参数 / Command payload (required): keys are command names (power: 0/1, brightness: 1-100)"},
					},
					"required": []string{"id", "command"},
				},
				"annotations": map[string]any{"readOnlyHint": false},
			},
		},
	})
}

// ---------- invoke ----------

// invokeBody invoke 请求体。
type invokeBody struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// invoke POST /v1/tools/invoke：{name, arguments} → 200 {"result":...} / 非 200 problem+json。
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
		writeProblem(w, http.StatusUnauthorized, 40101, "Unauthorized", "invalid upstream credential")
		return
	}
	var req invokeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", `body must be a JSON object: {"name":"...","arguments":{...}}`)
		return
	}
	var args map[string]any
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "arguments must be a JSON object")
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch req.Name {
	case "list_devices":
		s.toolListDevices(w, args)
	case "get_device_overview":
		s.toolGetDeviceOverview(w, args)
	case "control_device":
		s.toolControlDevice(w, args)
	default:
		writeProblem(w, http.StatusNotFound, 40401, "Not Found", "unknown tool: "+req.Name)
	}
}

// toolListDevices cursor 分页（id 升序；cursor = base64url(末条 id)，空 next_cursor 终止）。
func (s *Server) toolListDevices(w http.ResponseWriter, args map[string]any) {
	limit := 20
	if v, ok := args["limit"]; ok {
		f, ok := v.(float64)
		if !ok {
			writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "limit must be an integer")
			return
		}
		limit = int(f)
		if limit < 1 {
			limit = 1
		}
		if limit > 100 {
			limit = 100
		}
	}
	var after int64
	if c, _ := args["cursor"].(string); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "invalid cursor encoding")
			return
		}
		after, err = strconv.ParseInt(string(raw), 10, 64)
		if err != nil || after <= 0 {
			writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "invalid cursor value")
			return
		}
	}
	q, _ := args["q"].(string)

	var page []*device
	for id := after + 1; id <= after+1000; id++ { // 演示数据量极小，线性扫即可
		d, ok := s.devs[id]
		if !ok {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(d.Name), strings.ToLower(q)) {
			continue
		}
		page = append(page, d)
		if len(page) > limit {
			break
		}
	}
	next := ""
	if len(page) > limit {
		page = page[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(page[limit-1].ID, 10)))
	}
	list := make([]any, 0, len(page))
	for _, d := range page {
		list = append(list, deviceJSON(d))
	}
	writeResult(w, map[string]any{"data": list, "next_cursor": next})
}

// toolGetDeviceOverview 设备全貌：档案 + 实时状态。
func (s *Server) toolGetDeviceOverview(w http.ResponseWriter, args map[string]any) {
	d, ok := s.argDevice(w, args)
	if !ok {
		return
	}
	writeResult(w, map[string]any{"data": map[string]any{
		"device_id": d.ID,
		"device":    deviceJSON(d),
		"state":     stateJSON(d),
	}})
}

// toolControlDevice 下发命令（仅 light 可控；sent=1 表示已被模拟器受理）。
func (s *Server) toolControlDevice(w http.ResponseWriter, args map[string]any) {
	d, ok := s.argDevice(w, args)
	if !ok {
		return
	}
	cmd, ok := args["command"].(map[string]any)
	if !ok || len(cmd) == 0 {
		writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", `command must be a non-empty JSON object (e.g. {"power":1})`)
		return
	}
	if d.Kind != "light" {
		writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "device "+d.Name+" ("+d.Kind+") accepts no commands; only lights are controllable")
		return
	}
	applied := map[string]any{}
	for k, v := range cmd {
		switch k {
		case "power":
			f, isNum := v.(float64)
			if !isNum || (f != 0 && f != 1) {
				writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "command power must be 0 or 1")
				return
			}
			d.Power = f == 1
			applied["power"] = f
		case "brightness":
			f, isNum := v.(float64)
			if !isNum || f < 1 || f > 100 {
				writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "command brightness must be an integer in 1-100")
				return
			}
			d.Brightness = int(f)
			applied["brightness"] = int(f)
		default: // 非法键忽略（语义陷阱已在工具描述中前置说明）
		}
	}
	writeResult(w, map[string]any{"data": map[string]any{
		"device_id": d.ID,
		"sent":      1,
		"applied":   applied,
	}})
}

// argDevice 取 id 参数并查设备；失败已写响应，ok=false。
func (s *Server) argDevice(w http.ResponseWriter, args map[string]any) (*device, bool) {
	f, ok := args["id"].(float64)
	if !ok {
		writeProblem(w, http.StatusBadRequest, 40001, "Bad Request", "missing required argument: id (integer)")
		return nil, false
	}
	d, ok := s.devs[int64(f)]
	if !ok {
		writeProblem(w, http.StatusNotFound, 40401, "Not Found", "device "+strconv.FormatInt(int64(f), 10)+" not found")
		return nil, false
	}
	return d, true
}

// ---------- 视图与响应 ----------

// deviceJSON 设备档案视图。
func deviceJSON(d *device) map[string]any {
	return map[string]any{"id": d.ID, "name": d.Name, "kind": d.Kind}
}

// stateJSON 实时状态视图（在线标志 + 类型相关字段）。
func stateJSON(d *device) map[string]any {
	st := map[string]any{
		"online":    d.Online,
		"last_seen": time.Now().UTC().Format(time.RFC3339),
	}
	if d.Kind == "light" {
		st["power"] = d.Power
		st["brightness"] = d.Brightness
	} else {
		st["temperature"] = d.Temperature
		st["humidity"] = d.Humidity
	}
	return st
}

// writeResult 200 {"result":...}（契约成功形态）。
func writeResult(w http.ResponseWriter, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// writeProblem 非 200 problem+json（契约错误形态：type/title/status/code/detail）。
func writeProblem(w http.ResponseWriter, status, code int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  title,
		"status": status,
		"code":   code,
		"detail": detail,
	})
}

// writeJSON 普通 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
