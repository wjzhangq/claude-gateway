package ipgeo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// LookupCity resolves an IP to a Chinese city name via the ip9.com.cn service.
// It returns the city, falling back to province then country when the city
// field is empty (e.g. some overseas or carrier IPs). Reserved/internal or
// invalid IPs (ret != 200, or "内网地址") yield an empty string with no error.
//
// The service requires no API key. Response shape:
//
//	{"ret":200,"data":{"prov":"北京","city":"北京","country":"中国",...}}
//	{"ret":400,"data":[]}                       // invalid IP
//	{"ret":200,"data":{"country":"保留","isp":"内网地址",...}} // reserved
func LookupCity(client *http.Client, ip string) (string, error) {
	endpoint := "https://ip9.com.cn/get?ip=" + url.QueryEscape(ip)
	resp, err := client.Get(endpoint)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// data is an object on success and an empty array on error, so decode it
	// lazily and only interpret it when ret == 200.
	var envelope struct {
		Ret  int             `json:"ret"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return "", err
	}
	if envelope.Ret != 200 {
		return "", fmt.Errorf("ip9 returned ret=%d for %s", envelope.Ret, ip)
	}

	var data struct {
		Country string `json:"country"`
		Prov    string `json:"prov"`
		City    string `json:"city"`
		ISP     string `json:"isp"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		// data was not an object (e.g. []) — treat as unresolved, not fatal.
		return "", nil
	}
	// Reserved / internal addresses carry no meaningful location.
	if data.Country == "保留" || data.ISP == "内网地址" {
		return "", nil
	}
	if data.City != "" {
		return data.City, nil
	}
	if data.Prov != "" {
		return data.Prov, nil
	}
	return data.Country, nil
}
