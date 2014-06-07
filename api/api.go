package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

type IP net.IP

func (a *IP) UnmarshalJSON(data []byte) error {
	*a = IP(net.ParseIP(strings.Trim(string(data), "\"")))
	return nil
}

func (a IP) String() string {
	return net.IP(a).String()
}

type Server struct {
	Server_ip     IP     // (String)     Server main IP address
	Server_number int    // (Integer)	 Server id
	Server_name   string // (String)	 Server name
	Product       string // (String)	 Server product name
	Dc            string // (String)	 Datacentre number
	Traffic       string // (String)	 Free traffic quota
	Flatrate      bool   // (Boolean)	 Indicates if the server has a traffic flatrate (traffic overusage will not be charged but the bandwith will be reduced) or not (traffic overusage will be charged)
	Status        string // (String)	 Server order status (ready or in process)
	Throttled     bool   // (Boolean)	 Bandwidth limit status
	Cancelled     bool   // (Boolean)	 Status of server cancellation
	Paid_until    string // (String)	 Paid until date
}

type Ip struct {
	Ip               IP     // (String)	 IP address
	Server_ip        IP     // (String)	 Server main IP address
	Server_number    int    // (Integer) Server id
	Locked           bool   // (Boolean) Status of locking
	Separate_mac     string // (String)	 Separate MAC address, if not set null
	Traffic_warnings bool   // (Boolean) True if traffic warnings are enabled
	Traffic_hourly   int    // (Integer) Hourly traffic limit in MB
	Traffic_daily    int    // (Integer) Daily traffic limit in MB
	Traffic_monthly  int    // (Integer) Monthly traffic limit in GB
}

type Subnet struct {
	Ip               IP   // (String)	IP address
	Mask             int  // (Integer)	Subnet mask in CIDR notation
	Gateway          IP   // (String)	Subnet gateway
	Server_ip        IP   // (String)	Server main IP address
	Server_number    int  // (Integer)	Server id
	Failover         bool // (Boolean)	True if net is a failover net
	Locked           bool // (Boolean)	Status of locking
	Traffic_warnings bool // (Boolean)	True if traffic warnings are enabled
	Traffic_hourly   int  // (Integer)	Hourly traffic limit in MB
	Traffic_daily    int  // (Integer)	Daily traffic limit in MB
	Traffic_monthly  int  // (Integer)	Monthly traffic limit in GB
}

type Rdns struct {
	Ip  IP     // (String)	 IP address
	Ptr string // (String)	 PTR record
}

type Failover struct {
	Ip               IP     // (String)	IP address
	Netmask          string // (String) Failover netmask
	Server_ip        IP     // (String)	Server main IP address
	Server_number    int    // (Integer) Server id
	Active_server_ip IP     // (String)	Main IP of current destination server
}

type Error struct {
	Status  int    // (Integer) HTTP Status Code
	Code    string // (String) Specific error code
	Message string // (String) Specific error message
}

func (e Error) Error() string {
	return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
}

type RequestError struct {
	HttpReq        string
	HttpMethod     string
	HttpStatusCode int
	RootCause      error
}

func (e RequestError) Error() string {
	return fmt.Sprintf("hetzner: %s %s got '%d %s' (cause: %v)", e.HttpReq, e.HttpMethod, e.HttpStatusCode, http.StatusText(e.HttpStatusCode), e.RootCause)
}

type Client struct {
	http            *http.Client
	baseUrl         string
	login, password string
}

func decodeResponse(res *http.Response, v interface{}) error {
	// check value type before any transfer
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		panic("v is nil")
	}
	sliceWanted := rv.Kind() == reflect.Ptr && rv.Elem().Kind() == reflect.Slice

	// get type of v' element
	var vtyp reflect.Type
	if sliceWanted {
		vtyp = rv.Elem().Type().Elem()
	} else {
		vtyp = rv.Elem().Type()
	}
	// construct slice of v' objects wrapped in map, as hetzner api returns json object like [{server:{/*fields*/}}]
	key := reflect.ValueOf(strings.ToLower(vtyp.Name()))
	var ptr reflect.Value
	if sliceWanted {
		ptr = reflect.New(reflect.SliceOf(reflect.MapOf(key.Type(), vtyp)))
	} else {
		ptr = reflect.New(reflect.MapOf(key.Type(), vtyp))
	}
	// decode json
	dec := json.NewDecoder(res.Body)
	if err := dec.Decode(ptr.Interface()); err != nil {
		return err
	}

	if sliceWanted {
		// construct the new slice of unwrapped v' objects [{/*fields*/}]
		uSlice := reflect.MakeSlice(reflect.SliceOf(vtyp), 0, ptr.Elem().Len())

		// fill new slice with unwrapped v' objects
		wSlice := ptr.Elem()
		for i := 0; i < wSlice.Len(); i++ {
			uSlice = reflect.Append(uSlice, wSlice.Index(i).MapIndex(key))
		}

		// make v point to slice with unwrapped values
		rv.Elem().Set(uSlice)
	} else {
		rv.Elem().Set(ptr.Elem().MapIndex(key))
	}

	return nil
}

func (c *Client) do(req *http.Request, v interface{}) error {
	res, err := c.http.Do(req)
	defer res.Body.Close()
	newRequestError := func(cause error) error { return &RequestError{req.Method, req.URL.Path, res.StatusCode, cause} }
	if err != nil {
		return newRequestError(err)
	}
	if res.StatusCode != http.StatusOK {
		// decode hetzner error from response body
		var cause Error
		if err = decodeResponse(res, &cause); err == nil {
			err = cause
		}
		return newRequestError(err)
	}
	// decode response object
	if err = decodeResponse(res, v); err != nil {
		return newRequestError(err)
	}
	return nil
}

func (c *Client) Get(method string, v interface{}) error {
	// do request
	req, _ := http.NewRequest("GET", c.baseUrl+method, nil)
	req.SetBasicAuth(c.login, c.password)

	return c.do(req, v)
}

func (c *Client) Post(method string, params url.Values, v interface{}) error {
	// do request
	req, _ := http.NewRequest("POST", c.baseUrl+method, strings.NewReader(params.Encode()))
	req.SetBasicAuth(c.login, c.password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return c.do(req, v)
}

var DefaultClient = &Client{http.DefaultClient, "https://robot-ws.your-server.de", "", ""}

func SetBasicAuth(username, password string) {
	DefaultClient.login, DefaultClient.password = username, password
}

func Get(method string, v interface{}) error {
	return DefaultClient.Get(method, v)
}

func Post(method string, params url.Values, v interface{}) error {
	return DefaultClient.Post(method, params, v)
}
