package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

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

type LeagueEntry struct {
	LeaguePoints int `json:"leaguePoints"`
}

type LeagueResponse struct {
	Entries []LeagueEntry `json:"entries"`
}

//go:embed cutoffs.yaml
var cutoffsYAML []byte

const (
	baseURL          = "api.riotgames.com"
	minChallengerLP  = 500
	minGrandmasterLP = 200

	queueTypeSoloDuo = "RANKED_SOLO_5x5"
	queueTypeFlex    = "RANKED_FLEX_SR"

	leagueTypeChallenger  = "challengerleagues"
	leagueTypeGrandmaster = "grandmasterleagues"
	leagueTypeMaster      = "masterleagues"
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

type LeagueDataResult struct {
	LeagueType string
	QueueType  string
	Response   LeagueResponse
	Err        error
}

func main() {
	apiKey := os.Getenv("RIOT_API_KEY")
	if apiKey == "" {
		log.Fatal("RIOT_API_KEY environment variable is required")
	}

	var cfg config
	if err := yaml.Unmarshal(cutoffsYAML, &cfg); err != nil {
		log.Fatalf("Failed to unmarshal cutoffs.yaml: %v", err)
	}

	for {
		outputData := make(map[string]RegionData)
		resultChan := make(chan RegionResult, len(cfg.Regions))
		var wg sync.WaitGroup

		for region, regionCfg := range cfg.Regions {
			wg.Add(1)
			go func(region string, regionCfg Queues) {
				defer wg.Done()
				data, err := processRegion(region, regionCfg, apiKey)
				resultChan <- RegionResult{Region: region, Data: data, Err: err}
			}(region, regionCfg)
		}

		wg.Wait()
		close(resultChan)

		for result := range resultChan {
			if result.Err != nil {
				log.Printf("Error processing region %s: %v", result.Region, result.Err)
				continue
			}
			outputData[result.Region] = result.Data
			logRegionCutoffs(result.Region, result.Data)
		}

		if err := writeCutoffsToFiles(outputData); err != nil {
			log.Printf("Error writing cutoffs to files: %v", err)
		}

		time.Sleep(1 * time.Minute)
	}
}

func logRegionCutoffs(region string, data RegionData) {
	log.Printf("Region: %s\n", region)
	log.Printf("Challenger Solo/Duo: %d\n", data.RANKED_SOLO_5x5.Challenger)
	log.Printf("Grandmaster Solo/Duo: %d\n", data.RANKED_SOLO_5x5.Grandmaster)
	log.Printf("Challenger Flex: %d\n", data.RANKED_FLEX_SR.Challenger)
	log.Printf("Grandmaster Flex: %d\n", data.RANKED_FLEX_SR.Grandmaster)
	log.Println()
}

func writeCutoffsToFiles(outputData map[string]RegionData) error {
	jsonData, err := json.MarshalIndent(outputData, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	if err := ensureDir("cdn/current"); err != nil {
		return err
	}
	if err := writeFile("cdn/current/cutoffs.json", jsonData); err != nil {
		return err
	}

	currentDate := time.Now().UTC().Format("2006-01-02")
	dirPath := fmt.Sprintf("cdn/%s", currentDate)
	if err := ensureDir(dirPath); err != nil {
		return err
	}
	if err := writeFile(fmt.Sprintf("%s/cutoffs.json", dirPath), jsonData); err != nil {
		return err
	}
	return nil
}

func ensureDir(dirPath string) error {
	if _, err := os.Stat(dirPath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dirPath, err)
		}
	}
	return nil
}

func writeFile(filePath string, data []byte) error {
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("write file to %s: %w", filePath, err)
	}
	return nil
}

func processRegion(region string, regionCfg Queues, apiKey string) (RegionData, error) {
	leagueTypes := []struct {
		LeagueType string
		QueueType  string
	}{
		{leagueTypeChallenger, queueTypeSoloDuo},
		{leagueTypeGrandmaster, queueTypeSoloDuo},
		{leagueTypeMaster, queueTypeSoloDuo},
		{leagueTypeChallenger, queueTypeFlex},
		{leagueTypeGrandmaster, queueTypeFlex},
		{leagueTypeMaster, queueTypeFlex},
	}

	var fetchErrors []error
	leagueResponses := make(map[string]LeagueResponse)

	for _, leagueFetch := range leagueTypes {
		resp, err := fetchLeagueData(region, leagueFetch.LeagueType, leagueFetch.QueueType, apiKey)
		if err != nil {
			fetchErrors = append(fetchErrors, fmt.Errorf("fetchLeagueData %s %s for %s failed: %w",
				leagueFetch.LeagueType, leagueFetch.QueueType, region, err))
		} else {
			leagueResponses[leagueFetch.QueueType+"_"+leagueFetch.LeagueType] = resp
		}
	}

	if len(fetchErrors) > 0 {
		combinedError := fmt.Errorf("errors fetching league data for region %s:", region)
		for _, err := range fetchErrors {
			combinedError = fmt.Errorf("%w\n%v", combinedError, err)
		}
		return RegionData{}, combinedError
	}

	soloLadder := createLadder(
		leagueResponses[queueTypeSoloDuo+"_"+leagueTypeChallenger],
		leagueResponses[queueTypeSoloDuo+"_"+leagueTypeGrandmaster],
		leagueResponses[queueTypeSoloDuo+"_"+leagueTypeMaster],
	)
	flexLadder := createLadder(
		leagueResponses[queueTypeFlex+"_"+leagueTypeChallenger],
		leagueResponses[queueTypeFlex+"_"+leagueTypeGrandmaster],
		leagueResponses[queueTypeFlex+"_"+leagueTypeMaster],
	)

	soloCutoffs := calculateCutoffs(soloLadder, regionCfg.SoloDuo)
	flexCutoffs := calculateCutoffs(flexLadder, regionCfg.Flex)

	return RegionData{
		RANKED_SOLO_5x5: soloCutoffs,
		RANKED_FLEX_SR:  flexCutoffs,
	}, nil
}

func createLadder(challengerLeague, grandmasterLeague, masterLeague LeagueResponse) []LeagueEntry {
	ladder := append(append(challengerLeague.Entries, grandmasterLeague.Entries...), masterLeague.Entries...)
	sort.Slice(ladder, func(i, j int) bool {
		return ladder[i].LeaguePoints > ladder[j].LeaguePoints
	})
	return ladder
}

func calculateCutoffs(ladder []LeagueEntry, cutoffsConfig Cutoffs) Cutoffs {
	challenger := minChallengerLP
	grandmaster := minGrandmasterLP

	if len(ladder) >= cutoffsConfig.Challenger {
		challenger = int(math.Max(float64(minChallengerLP), float64(ladder[cutoffsConfig.Challenger-1].LeaguePoints)))
	}
	if len(ladder) >= cutoffsConfig.Challenger+cutoffsConfig.Grandmaster {
		grandmaster = int(math.Max(float64(minGrandmasterLP), float64(ladder[cutoffsConfig.Challenger+cutoffsConfig.Grandmaster-1].LeaguePoints)))
	}

	return Cutoffs{
		Challenger:  challenger,
		Grandmaster: grandmaster,
	}
}

func fetchLeagueData(region string, league string, queueType string, apiKey string) (LeagueResponse, error) {
	url := fmt.Sprintf("https://%s.%s/lol/league/v4/%s/by-queue/%s?api_key=%s", region, baseURL, league, queueType, apiKey)
	resp, err := http.Get(url)
	if err != nil {
		return LeagueResponse{}, fmt.Errorf("HTTP GET error for %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return LeagueResponse{}, fmt.Errorf("API request failed with status code: %d for URL: %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return LeagueResponse{}, fmt.Errorf("failed to read response body for %s: %w", url, err)
	}

	var leagueData LeagueResponse
	if err := json.Unmarshal(body, &leagueData); err != nil {
		return LeagueResponse{}, fmt.Errorf("failed to unmarshal response body for %s: %w - body: %s", url, err, string(body))
	}

	return leagueData, nil
}
