package main

import (
	"util"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/garyburd/redigo/redis"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const KEY_SPEED_INFO_PRE = "speed_info"
const KEY_CLIENT_STATUS_PRE = "client_status" // 机器上线/下线列表
const KEY_CLIENT_DHCP_NAMES = "client_names"  // 机器MAC对应的DHCP分配的名称
const KEY_ONLINE_CLIENTT = "online_client"    // 在线的机器
type Config struct {
	SpeedUrl       string
	DhcpUrl        string
	Cookie         string
	Referer        string
	SleepSec       time.Duration // 采集间隔时间，单位秒
	HttpServerPort uint16
	LogFile        string
}

func parse_config(file_path string) Config {
	buff, err := ioutil.ReadFile(file_path)
	if err != nil {
		panic(err)
	}
	var config Config
	err = json.Unmarshal([]byte(string(buff)), &config)
	if err != nil {
		panic(err)
	}
	return config
}

var logger *log.Logger

func main() {
	run_type := flag.String("t", "setter", "speed_monitor run type, setter or getter")
	flag.Parse()

	// 初始化redis连接
	conn := init_redis_client()
	defer conn.Close()

	info := speed_infos{}
	info.names = map[string]string{}
	info.conn = conn
	info.config = parse_config("../conf/speed_monitor.conf")

	// 日志文件
	logFile, err := os.Create(info.config.LogFile)
	defer logFile.Close()
	if err != nil {
		log.Fatalln("open log file fail")
	}
	logger = log.New(logFile, "", log.Lshortfile)

	if *run_type == "setter" {
		runtime.GOMAXPROCS(2)
		go info.save_client_info()
		go info.start_http_server()
		ch := make(chan int)
		<-ch
	} else if *run_type == "test" {
		info.get_all_dhcp_client()
	} else {
		fmt.Println("wrong type")
	}
}

type machine struct {
	ip         string
	mac        string
	down_speed uint64
	up_speed   uint64
}

type speed_infos struct {
	machines       []machine
	names          map[string]string // key: mac, value:machine name
	online_clients map[string]bool
	config         Config
	conn           redis.Conn
}

func (infos *speed_infos) get_all_dhcp_client() {
	dhcp_str := infos.request(infos.config.DhcpUrl)
	re, _ := regexp.Compile("var DHCPDynList=new Array\\(\n((.*\n)*?)0,0 \\);\n")
	submath := re.FindSubmatch([]byte(dhcp_str))
	if len(submath) == 3 {
		dhcp_clients_str := string(submath[1])
		items := strings.Split(dhcp_clients_str, ",\n")
		for i := 0; i+1 < len(items); i += 4 {
			mac := strings.Replace(items[i+1], "\"", "", -1)
			name := strings.Replace(items[i], "\"", "", -1)
			infos.names[mac] = name
			infos.conn.Do("HSET", KEY_CLIENT_DHCP_NAMES, mac, name)
		}
	}
}

func (infos *speed_infos) get_speed(w http.ResponseWriter, req *http.Request) {
	// 取得所有的key
	keys, err := redis.Strings(infos.conn.Do("KEYS", KEY_SPEED_INFO_PRE+":*"))
	if err != nil {
		panic(err)
	}
	output := []string{}
	for _, key := range keys {
		info, _ := redis.String(infos.conn.Do("LINDEX", key, 0))
		item := strings.Split(info, "|")
		mac := strings.TrimLeft(key, KEY_SPEED_INFO_PRE+":")
		name, _ := redis.String(infos.conn.Do("HGET", KEY_CLIENT_DHCP_NAMES, mac))
		raw_up, _ := strconv.ParseUint(item[2], 10, 64)
		raw_down, _ := strconv.ParseUint(item[3], 10, 64)
		up_speed := get_readable_speed_str(raw_up)
		down_speed := get_readable_speed_str(raw_down)
		output = append(output, fmt.Sprintf("name:%20v|up_speed:%10v|down_speed:%10v",
			name, up_speed, down_speed))
	}
	io.WriteString(w, strings.Join(output, "\n"))
}

func (infos *speed_infos) start_http_server() {
	http.HandleFunc("/get_speed", infos.get_speed)
	addr := fmt.Sprintf(":%v", infos.config.HttpServerPort)
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		panic(err)
	}
}

func (infos *speed_infos) save_client_info() {
	for {
		infos.parse_speed()
		now := time.Now().Unix()
		old_clients := infos.online_clients
		infos.online_clients = map[string]bool{}
		for _, machine := range infos.machines {
			infos.online_clients[machine.mac] = true
			if machine.up_speed == 0 && machine.down_speed == 0 {
				continue
			}
			key := KEY_SPEED_INFO_PRE + ":" + machine.mac
			value := fmt.Sprintf("%v|%v|%v|%v", now, machine.ip, machine.up_speed,
				machine.down_speed)
			logger.Printf("save speed to redis, key[%v] value[%v]\n", key, value)
			infos.conn.Do("LPUSH", key, value)
		}
		// 保存当前在线的机器MAC
		infos.conn.Do("DEL", KEY_ONLINE_CLIENTT)
		for mac, _ := range infos.online_clients {
			logger.Printf("add client[%v] to key [%v]\n", mac, KEY_ONLINE_CLIENTT)
			infos.conn.Do("SADD", KEY_ONLINE_CLIENTT, mac)
		}
		// 检查上线/下线的机器，写入redis,暂时忽略第一次运行的情况
		online_clients := util.Map_diff(infos.online_clients, old_clients)
		offline_clients := util.Map_diff(old_clients, infos.online_clients)
		for mac, _ := range online_clients {
			// key[client_status:ab-cd-xxxx] value[1446561630|1]
			key := fmt.Sprintf("%v:%v", KEY_CLIENT_STATUS_PRE, mac)
			value := fmt.Sprintf("%v|1", now)
			infos.conn.Do("LPUSH", key, value)
		}
		for mac, _ := range offline_clients {
			// key[client_status:ab-cd-xxxx] value[1446561630|0]
			key := fmt.Sprintf("%v:%v", KEY_CLIENT_STATUS_PRE, mac)
			value := fmt.Sprintf("%v|0", now)
			infos.conn.Do("LPUSH", key, value)
		}

		infos.get_all_dhcp_client()

		time.Sleep(infos.config.SleepSec * time.Second)
	}
}

func (infos *speed_infos) parse_speed() {
	speed_str := infos.request(infos.config.SpeedUrl)
	strs := strings.SplitN(speed_str, "</script>", 2)
	strs = strings.SplitN(strs[0], "Array(\n", 2)
	strs = strings.SplitN(strs[1], "0,0 );", 2)
	strs = strings.Split(strs[0], "\n")
	infos.machines = infos.machines[:0]
	for _, line := range strs {
		item := strings.Split(line, ",")
		if len(item) < 7 {
			break
		}
		info := machine{}
		info.ip = strings.Replace(item[1], "\"", "", -1)
		info.mac = strings.Replace(item[2], "\"", "", -1)
		down, err := strconv.ParseUint(item[5], 10, 64)
		if err == nil {
			info.down_speed = down
		}
		up, err := strconv.ParseUint(item[6], 10, 64)
		if err == nil {
			info.up_speed = up
		}
		infos.machines = append(infos.machines, info)
	}
}

// 转换速度为可读格式（B/s KB/s MB/s）
func get_readable_speed_str(speed uint64) (str string) {
	speed_f := float64(speed)
	if speed/1024/1024 > 0 {
		str = fmt.Sprintf("%.2f MB/s", speed_f/1024/1024)
	} else if speed/1024 > 0 {
		str = fmt.Sprintf("%.2f KB/s", speed_f/1024)
	} else {
		str = fmt.Sprintf("%.2f B/s", speed_f)
	}
	return
}

func (infos speed_infos) String() (str string) {
	machine_num := len(infos.machines)
	if machine_num == 0 {
		str = "speed info empty"
	} else {
		word := "machine"
		if machine_num > 1 {
			word += "s"
		}
		str = fmt.Sprintf("there are %v %v\n", machine_num, word)
		for _, info := range infos.machines {
			str += fmt.Sprintf("[ip: %v, mac: %v, down: %v, up: %v]\n",
				info.ip, info.mac, get_readable_speed_str(info.down_speed),
				get_readable_speed_str(info.up_speed))
		}
	}
	return
}

func (info *speed_infos) request(url string) string {
	client := &http.Client{}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Cookie", info.config.Cookie)
	req.Header.Set("Referer", info.config.Referer)

	resp, err := client.Do(req)
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	return string(body)
}

func init_redis_client() redis.Conn {
	c, err := redis.Dial("tcp", ":6379")
	if err != nil {
		panic(err)
	}

	return c
}
