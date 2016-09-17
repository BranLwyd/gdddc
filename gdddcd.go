package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"time"
)

var (
	configFile = flag.String("config_file", "gdddcd.config",
		"File used to track configuration.")
	stateFile = flag.String("state_file", "gdddcd.state",
		"File used to track state.")

	ipRe = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
)

// config stores read-only configuration information.
type config struct {
	Hostname        string  `json:"hostname"`
	Username        string  `json:"username"`
	Password        string  `json:"password"`
	UpdateFrequency float64 `json:"update_freq_s"`
	IPCheckURL      string  `json:"ip_check_url"`
	UserAgent       string  `json:"user_agent"`
}

// state stores read-write information.
type state struct {
	IP string `json:"ip"`
}

// readConfig reads the config off the disk and returns it; it will fill in default values for unspecified fields.
func readConfig() (*config, error) {
	// Read config off disk.
	configBytes, err := ioutil.ReadFile(*configFile)
	if err != nil {
		return nil, fmt.Errorf("could not read config: %v", err)
	}
	c := &config{}
	if err := json.Unmarshal(configBytes, c); err != nil {
		return nil, fmt.Errorf("could not parse config: %v", err)
	}

	// Check required fields.
	if c.Hostname == "" {
		return nil, fmt.Errorf("hostname is a required field")
	}
	if c.Username == "" {
		return nil, fmt.Errorf("username is a required field")
	}
	if c.Password == "" {
		return nil, fmt.Errorf("password is a required field")
	}

	// Fill defaults for unspecified fields.
	if c.UpdateFrequency <= 0 {
		log.Printf("update_freq_s unspecified (or negative) in config, using default of 60")
		c.UpdateFrequency = 60
	}
	if c.IPCheckURL == "" {
		log.Printf("ip_check_url unspecified in config, using default of https://domans.google.com/checkip")
		c.IPCheckURL = "https://domains.google.com/checkip"
	}
	if c.UserAgent == "" {
		log.Printf("user_agent unspecified in config, using default of gdddcd 1.0")
		c.UserAgent = "gdddcd 1.0"
	}

	return c, nil
}

// readState reads the state off the disk and returns it.
func readState() (*state, error) {
	stateBytes, err := ioutil.ReadFile(*stateFile)
	if err != nil {
		return nil, fmt.Errorf("could not read state: %v", err)
	}
	s := &state{}
	if err := json.Unmarshal(stateBytes, s); err != nil {
		return nil, fmt.Errorf("could not parse state: %v", err)
	}
	return s, nil
}

// write writes the state to disk.
func (s *state) write() error {
	stateBytes, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("could not marshal state: %v", err)
	}
	if err := ioutil.WriteFile(*stateFile, stateBytes, 0600); err != nil {
		return fmt.Errorf("could not write state: %v", err)
	}
	return nil
}

// checkIP gets the IP address from the config-specified IP check URL.
func checkIP(cfg *config) (string, error) {
	req, err := http.NewRequest("GET", cfg.IPCheckURL, nil)
	if err != nil {
		return "", fmt.Errorf("could not create request: %v", err)
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not make request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP error: %v", resp.Status)
	}
	ip, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("could not read IP: %v", err)
	}
	if !ipRe.Match(ip) {
		return "", fmt.Errorf("response not IP-shaped: %v", string(ip))
	}
	return string(ip), nil
}

// updateIP uses the given configuration to update the current IP with Google Domains.
func updateIP(cfg *config, newIP string) error {
	url := fmt.Sprintf("https://%s:%s@domains.google.com/nic/update?hostname=%s&myip=%s", cfg.Username, cfg.Password, cfg.Hostname, newIP)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return fmt.Errorf("could not create request: %v", err)
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not make make request: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("could not read response: %v", err)
	}
	body := string(bodyBytes)
	if body == fmt.Sprintf("good %s", newIP) {
		return nil
	}
	if resp.StatusCode == 200 {
		log.Printf("IP update got unexpected response body for successful update: %q", body)
		return nil
	}
	return fmt.Errorf("IP update got error: %q (%v)", body, resp.Status)
}

func main() {
	// Read flags, config, & state.
	flag.Parse()
	cfg, err := readConfig()
	if err != nil {
		log.Fatalf("Could not read config: %v", err)
	}
	s, err := readState()
	if err != nil {
		log.Fatalf("Could not read state: %v", err)
	}

	// googIP tracks our conception of what Google thinks our IP is.
	// It normally differs from the state IP only briefly between updating the goog IP and the state.
	// It may differ for a longer period of time if there are errors writing the new state.
	googIP := s.IP
	updateFreq := time.Duration(cfg.UpdateFrequency * float64(time.Second))
	log.Printf("Starting: will check & update IP every %v", updateFreq)
	for range time.Tick(updateFreq) {
		// Get current IP from service.
		log.Printf("Checking IP")
		curIP, err := checkIP(cfg)
		if err != nil {
			log.Printf("Could not check IP: %v", err)
			continue
		}

		// Update Google IP if needed.
		if curIP != googIP {
			log.Printf("Detected new IP (%v -> %v), updating", googIP, curIP)
			if err := updateIP(cfg, curIP); err != nil {
				log.Printf("Could not update IP: %v", err)
				continue
			}
			googIP = curIP
		}

		// Update state IP if needed.
		if curIP != s.IP {
			newS := *s
			newS.IP = curIP
			if err := newS.write(); err != nil {
				log.Printf("Could not update on-disk state: %v", err)
				continue
			}
			s = &newS
		}
	}
}
