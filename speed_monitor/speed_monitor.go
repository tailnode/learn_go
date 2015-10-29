package main

import (
    "fmt"
    "net/http"
    "strings"
	"io/ioutil"
	"strconv"
	"encoding/json"
	"github.com/garyburd/redigo/redis"
	"time"
	"flag"
)

type Config struct {
	SpeedUrl string
	NameUrl string
	Cookie string
	Referer string
	SleepSec time.Duration	// 采集间隔时间，单位秒
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
func sayhelloName(w http.ResponseWriter, r *http.Request) {
    r.ParseForm()  //解析参数，默认是不会解析的
    fmt.Println(r.Form)  //这些信息是输出到服务器端的打印信息
    fmt.Println("path", r.URL.Path)
    fmt.Println("scheme", r.URL.Scheme)
    fmt.Println(r.Form["url_long"])
    for k, v := range r.Form {
        fmt.Println("key:", k)
        fmt.Println("val:", strings.Join(v, ""))
    }
	cookie := http.Cookie{Name: "username", Value: "ming"}
	http.SetCookie(w, &cookie)
    fmt.Fprintf(w, "Hello astaxie!") //这个写入到w的是输出到客户端的
}

func main() {
	run_type := flag.String("t", "setter", "speed_monitor run type, setter or getter")
	flag.Parse()
	conn := init_redis_client()
	defer conn.Close()
	info := speed_infos{}
	info.conn = conn
	info.config = parse_config("./speed_monitor.conf")
	if *run_type == "setter" {
		info.get_and_save_speed()
	} else if *run_type == "getter" {
		
	} else {
		fmt.Println("wrong type")
	}
}

type machine struct {
	ip string
	mac string
	down_speed uint64
	up_speed uint64
}

type speed_infos struct {
	machines []machine
	config Config
	conn redis.Conn
}

func (infos* speed_infos) get_all_name() {
//	name_str := infos.request(infos.config.SpeedUrl)
}

func (infos* speed_infos) get_and_save_speed() {
	for {
		infos.get_all_speed()
		now := time.Now().Unix()
		type machines struct {
			machine []machine
		}
		for _, machine := range infos.machines {
			if machine.up_speed == 0 && machine.down_speed == 0 {
				continue
			}
			key := "speed_info:" + machine.mac
			value := fmt.Sprintf("%v|%v|%v|%v", now, machine.ip, machine.up_speed,
				machine.down_speed)
			fmt.Println("key: " + key)
			fmt.Println("value: " + value)
			infos.conn.Do("LPUSH", key, value)
		}
		time.Sleep(infos.config.SleepSec * time.Second)
	}
}

func (infos* speed_infos) get_all_speed() {
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
func get_readable_speed_str(speed uint64) (str string){
	speed_f := float64(speed)
	if speed / 1024 / 1024 > 0 {
		str = fmt.Sprintf("%.2f MB/s", speed_f / 1024 / 1024)
	} else if speed / 1024 > 0 {
		str = fmt.Sprintf("%.2f KB/s", speed_f / 1024)
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

func (info *speed_infos) request(url string) string{
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

func init_redis_client () redis.Conn {
	c, err := redis.Dial("tcp", ":6379")
	if err != nil {
		panic(err)
	}
	
	return c
}