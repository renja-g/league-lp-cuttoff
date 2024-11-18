import requests
import os
import json
import time
from concurrent.futures import ThreadPoolExecutor

RIOT_API_KEY = os.environ.get("RIOT_API_KEY")
if RIOT_API_KEY == "":
    print("RIOT_API_KEY not found")
    exit(1)

PLATFORMS = [
    "BR1",
    "EUN1",
    "EUW1",
    "JP1",
    "KR",
    "LA1",
    "LA2",
    # "ME1",  # TODO
    "NA1",
    "OC1",
    # "PH2",  # TODO
    "RU",
    # "SG2",  # TODO
    # "TH2",  # TODO
    # "TR1",
    # "TW2",  # TODO
    # "VN2",  # TODO
]

QUEUE_TYPES = [
    "RANKED_SOLO_5x5",
    "RANKED_FLEX_SR",
]

PLAYER_CUTOFF = {
    "BR1": {
        "RANKED_SOLO_5x5": {"grandmaster": 500, "challenger": 200},
        "RANKED_FLEX_SR": {"grandmaster": 500, "challenger": 200},
    },
    "EUN1": {
        "RANKED_SOLO_5x5": {"grandmaster": 500, "challenger": 200},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "EUW1": {
        "RANKED_SOLO_5x5": {"grandmaster": 700, "challenger": 300},
        "RANKED_FLEX_SR": {"grandmaster": 500, "challenger": 200},
    },
    "JP1": {
        "RANKED_SOLO_5x5": {"grandmaster": 100, "challenger": 50},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "KR": {
        "RANKED_SOLO_5x5": {"grandmaster": 700, "challenger": 300},
        "RANKED_FLEX_SR": {"grandmaster": 500, "challenger": 200},
    },
    "LA1": {
        "RANKED_SOLO_5x5": {"grandmaster": 500, "challenger": 200},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "LA2": {
        "RANKED_SOLO_5x5": {"grandmaster": 500, "challenger": 200},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "NA1": {
        "RANKED_SOLO_5x5": {"grandmaster": 700, "challenger": 300},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "OC1": {
        "RANKED_SOLO_5x5": {"grandmaster": 100, "challenger": 50},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "RU": {
        "RANKED_SOLO_5x5": {"grandmaster": 100, "challenger": 50},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
    "TR1": {
        "RANKED_SOLO_5x5": {"grandmaster": 500, "challenger": 200},
        "RANKED_FLEX_SR": {"grandmaster": 100, "challenger": 50},
    },
}

DEFAULT_CHALLENGER_CUTOFF = (
    500  # the LP cutoff for challenger will never be lower than 500
)
DEFAULT_GRANDMASTER_CUTOFF = (
    200  # the LP cutoff for grandmaster will never be lower than 200
)

CHELLANGER_URL = "https://{platform}.api.riotgames.com/lol/league/v4/challengerleagues/by-queue/{queue_type}?api_key={api_key}"
GRANDMASTER_URL = "https://{platform}.api.riotgames.com/lol/league/v4/grandmasterleagues/by-queue/{queue_type}?api_key={api_key}"
MASTER_URL = "https://{platform}.api.riotgames.com/lol/league/v4/masterleagues/by-queue/{queue_type}?api_key={api_key}"


def fetch_cutoff_data(platform, queue_type, api_key) -> tuple[str, str, int, int]:
    chellanger_url = CHELLANGER_URL.format(
        platform=platform, queue_type=queue_type, api_key=api_key
    )
    grandmaster_url = GRANDMASTER_URL.format(
        platform=platform, queue_type=queue_type, api_key=api_key
    )
    master_url = MASTER_URL.format(
        platform=platform, queue_type=queue_type, api_key=api_key
    )

    for _ in range(3):  # Retry up to 3 times
        try:
            chellanger_response = requests.get(chellanger_url)
            grandmaster_response = requests.get(grandmaster_url)
            master_response = requests.get(master_url)
            break
        except requests.exceptions.RequestException as e:
            print(f"Request failed: {e}. Retrying...")
            time.sleep(5)

    if (
        chellanger_response.status_code != 200
        or grandmaster_response.status_code != 200
        or master_response.status_code != 200
    ):
        print(
            f"Failed to fetch data for {platform}_{queue_type}. Status codes: {chellanger_response.status_code}, {grandmaster_response.status_code}, {master_response.status_code}"
        )
        exit(1)

    chellanger_data = chellanger_response.json()
    grandmaster_data = grandmaster_response.json()
    master_data = master_response.json()

    merge_data = (
        chellanger_data["entries"]
        + grandmaster_data["entries"]
        + master_data["entries"]
    )
    merge_data = sorted(merge_data, key=lambda x: x["leaguePoints"], reverse=True)

    chellanger_cutoff = PLAYER_CUTOFF[platform][queue_type]["challenger"]
    grandmaster_cutoff = (
        PLAYER_CUTOFF[platform][queue_type]["grandmaster"] + chellanger_cutoff
    )

    if len(merge_data) > grandmaster_cutoff:
        chellanger_cutoff = max(
            merge_data[chellanger_cutoff - 1]["leaguePoints"], DEFAULT_CHALLENGER_CUTOFF
        )
        grandmaster_cutoff = max(
            merge_data[grandmaster_cutoff - 1]["leaguePoints"], DEFAULT_GRANDMASTER_CUTOFF
        )
    else:
        chellanger_cutoff = DEFAULT_CHALLENGER_CUTOFF
        grandmaster_cutoff = DEFAULT_GRANDMASTER_CUTOFF

    return platform, queue_type, chellanger_cutoff, grandmaster_cutoff


lp_cutoff_data = {platform: {} for platform in PLATFORMS}

with ThreadPoolExecutor() as executor:
    futures = [
        executor.submit(fetch_cutoff_data, platform, queue_type, RIOT_API_KEY)
        for platform in PLATFORMS
        for queue_type in QUEUE_TYPES
    ]
    for future in futures:
        platform, queue_type, challenger_cutoff, grandmaster_cutoff = future.result()
        if platform not in lp_cutoff_data:
            lp_cutoff_data[platform] = {}
        lp_cutoff_data[platform][queue_type] = {
            "grandmaster": grandmaster_cutoff,
            "challenger": challenger_cutoff,
        }
        print(
            f"{platform}_{queue_type} - Challenger: {challenger_cutoff} - Grandmaster: {grandmaster_cutoff}"
        )

with open("lp_cutoffs.json", "w") as f:
    json.dump(lp_cutoff_data, f, indent=4)
    print("Data saved to lp_cutoffs.json")
