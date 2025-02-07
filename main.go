package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"

	"gopkg.in/yaml.v2"
)

type Cutoffs struct {
	Challenger  int `yaml:"challenger" json:"challenger"`
	Grandmaster int `yaml:"grandmaster" json:"grandmaster"`
}

type Queues struct {
	SoloDuo Cutoffs `yaml:"solo_duo" json:"RANKED_SOLO_5x5"`
	Flex    Cutoffs `yaml:"flex" json:"RANKED_FLEX_SR"`
}

type config struct {
	Regions map[string]Queues `yaml:",inline"`
}

type LeagueResponse struct {
	Entries []struct {
		LeaguePoints int `json:"leaguePoints"`
	} `json:"entries"`
}

//go:embed cutoffs.yaml
var cutoffsYAML []byte

const (
	baseUrl          = "api.riotgames.com"
	minChallengerLP  = 500
	minGrandmasterLP = 200
)

type RegionData struct {
	RANKED_SOLO_5x5 Cutoffs `json:"RANKED_SOLO_5x5"`
	RANKED_FLEX_SR  Cutoffs `json:"RANKED_FLEX_SR"`
}

type RegionResult struct {
	Region string
	Data   RegionData
	Err    error
}

func main() {
	apiKey := os.Getenv("RIOT_API_KEY")
	if apiKey == "" {
		log.Fatal("RIOT_API_KEY environment variable is required")
	}

	var cfg config
	err := yaml.Unmarshal(cutoffsYAML, &cfg)
	if err != nil {
		panic(err)
	}

	outputData := make(map[string]RegionData)
	resultChan := make(chan RegionResult, len(cfg.Regions)) // Buffered channel for results
	var wg sync.WaitGroup

	for region, regionCfg := range cfg.Regions {
		wg.Add(1)
		go func(region string, regionCfg Queues) {
			defer wg.Done()
			data, err := processRegion(region, regionCfg)
			resultChan <- RegionResult{Region: region, Data: data, Err: err}
		}(region, regionCfg)
	}

	wg.Wait()
	close(resultChan) // Close channel after all goroutines are done

	for result := range resultChan {
		if result.Err != nil {
			log.Printf("Error processing region %s: %v", result.Region, result.Err)
			continue
		}
		outputData[result.Region] = result.Data
		fmt.Printf("Region: %s\n", result.Region)
		fmt.Printf("Challenger Solo/Duo: %d\n", outputData[result.Region].RANKED_SOLO_5x5.Challenger)
		fmt.Printf("Grandmaster Solo/Duo: %d\n", outputData[result.Region].RANKED_SOLO_5x5.Grandmaster)
		fmt.Printf("Challenger Flex: %d\n", outputData[result.Region].RANKED_FLEX_SR.Challenger)
		fmt.Printf("Grandmaster Flex: %d\n", outputData[result.Region].RANKED_FLEX_SR.Grandmaster)
		fmt.Println()
	}

	// Save data to JSON file
	jsonData, err := json.MarshalIndent(outputData, "", "    ")
	if err != nil {
		log.Printf("Error marshaling JSON: %v", err)
		return
	}

	err = os.WriteFile("cutoffs.json", jsonData, 0644)
	if err != nil {
		log.Printf("Error writing JSON file: %v", err)
		return
	}
}

func processRegion(region string, regionCfg Queues) (RegionData, error) {
	type LeagueDataResult struct {
		LeagueType string
		QueueType  string
		Response   LeagueResponse
		Err        error
	}

	resultChan := make(chan LeagueDataResult, 6) // Buffered channel for 6 league data fetches
	var wg sync.WaitGroup

	leaguesToFetch := []struct {
		LeagueType string
		QueueType  string
	}{
		{"challengerleagues", "RANKED_SOLO_5x5"},
		{"grandmasterleagues", "RANKED_SOLO_5x5"},
		{"masterleagues", "RANKED_SOLO_5x5"},
		{"challengerleagues", "RANKED_FLEX_SR"},
		{"grandmasterleagues", "RANKED_FLEX_SR"},
		{"masterleagues", "RANKED_FLEX_SR"},
	}

	for _, leagueFetch := range leaguesToFetch {
		wg.Add(1)
		go func(leagueType, queueType string) {
			defer wg.Done()
			resp, err := fetchLeagueData(region, leagueType, queueType)
			resultChan <- LeagueDataResult{LeagueType: leagueType, QueueType: queueType, Response: resp, Err: err}
		}(leagueFetch.LeagueType, leagueFetch.QueueType)
	}

	wg.Wait()
	close(resultChan)

	leagueResponses := make(map[string]LeagueResponse) // Map to store LeagueResponses by QueueType
	var fetchErrors []error

	for result := range resultChan {
		if result.Err != nil {
			fetchErrors = append(fetchErrors, fmt.Errorf("fetchLeagueData %s %s for %s failed: %w", result.LeagueType, result.QueueType, region, result.Err))
			continue // Continue processing other results even if one fetch fails
		}
		leagueResponses[result.QueueType+"_"+result.LeagueType] = result.Response // Store response, using QueueType_LeagueType as key
	}

	if len(fetchErrors) > 0 {
		// Aggregate fetch errors and return
		combinedError := fmt.Errorf("errors fetching league data for region %s:", region)
		for _, err := range fetchErrors {
			combinedError = fmt.Errorf("%w\n%v", combinedError, err)
		}
		return RegionData{}, combinedError
	}

	CSoloLeague := leagueResponses["RANKED_SOLO_5x5_challengerleagues"]
	GMSoloLeague := leagueResponses["RANKED_SOLO_5x5_grandmasterleagues"]
	MSoloLeague := leagueResponses["RANKED_SOLO_5x5_masterleagues"]
	CFlexLeague := leagueResponses["RANKED_FLEX_SR_challengerleagues"]
	GMFlexLeague := leagueResponses["RANKED_FLEX_SR_grandmasterleagues"]
	MFlexLeague := leagueResponses["RANKED_FLEX_SR_masterleagues"]

	soloLadder := append(append(CSoloLeague.Entries, GMSoloLeague.Entries...), MSoloLeague.Entries...)
	flexLadder := append(append(CFlexLeague.Entries, GMFlexLeague.Entries...), MFlexLeague.Entries...)

	// Sort entries by LP in descending order
	sort.Slice(soloLadder, func(i, j int) bool {
		return soloLadder[i].LeaguePoints > soloLadder[j].LeaguePoints
	})
	sort.Slice(flexLadder, func(i, j int) bool {
		return flexLadder[i].LeaguePoints > flexLadder[j].LeaguePoints
	})

	// Handle small league lists and get LP thresholds
	soloChallenger := minChallengerLP
	soloGrandmaster := minGrandmasterLP
	flexChallenger := minChallengerLP
	flexGrandmaster := minGrandmasterLP

	if len(soloLadder) >= regionCfg.SoloDuo.Challenger {
		soloChallenger = int(math.Max(float64(minChallengerLP), float64(soloLadder[regionCfg.SoloDuo.Challenger-1].LeaguePoints)))
	}
	if len(soloLadder) >= regionCfg.SoloDuo.Challenger+regionCfg.SoloDuo.Grandmaster {
		soloGrandmaster = int(math.Max(float64(minGrandmasterLP), float64(soloLadder[regionCfg.SoloDuo.Challenger+regionCfg.SoloDuo.Grandmaster-1].LeaguePoints)))
	}

	if len(flexLadder) >= regionCfg.Flex.Challenger {
		flexChallenger = int(math.Max(float64(minChallengerLP), float64(flexLadder[regionCfg.Flex.Challenger-1].LeaguePoints)))
	}
	if len(flexLadder) >= regionCfg.Flex.Challenger+regionCfg.Flex.Grandmaster {
		flexGrandmaster = int(math.Max(float64(minGrandmasterLP), float64(flexLadder[regionCfg.Flex.Challenger+regionCfg.Flex.Grandmaster-1].LeaguePoints)))
	}

	return RegionData{
		RANKED_SOLO_5x5: Cutoffs{
			Challenger:  int(soloChallenger),
			Grandmaster: int(soloGrandmaster),
		},
		RANKED_FLEX_SR: Cutoffs{
			Challenger:  int(flexChallenger),
			Grandmaster: int(flexGrandmaster),
		},
	}, nil
}

func fetchLeagueData(region string, league string, queueType string) (LeagueResponse, error) {
	apiKey := os.Getenv("RIOT_API_KEY")
	url := fmt.Sprintf("https://%s.%s/lol/league/v4/%s/by-queue/%s?api_key=%s", region, baseUrl, league, queueType, apiKey)
	resp, err := http.Get(url)
	if err != nil {
		return LeagueResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return LeagueResponse{}, fmt.Errorf("API request failed with status code: %d for URL: %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return LeagueResponse{}, err
	}

	var leagueData LeagueResponse
	if err := json.Unmarshal(body, &leagueData); err != nil {
		return LeagueResponse{}, fmt.Errorf("failed to unmarshal response body: %w", err)
	}

	return leagueData, nil
}
