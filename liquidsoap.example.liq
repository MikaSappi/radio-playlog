# Define the URL and your API-key
playlog_url = "the.url.not"
playlog_key = "secrets"

def playlog_send(m) =
  title = m["title"]
  artist = m["artist"]

  # Check if you have data to send, fail if any is empty
  # (because you have to have both fields for reporting)

  if
    title != "" or artist != ""
  then
    # Don't break if the string contains `"` or `'` or `{}` and so on

    data = json.stringify({title = title, artist = artist})
    response =
      http.post(
        playlog_url,
        headers=[
          ("X-API-Key", playlog_key),
          ("Content-Type", "application/json")
        ],
        data=data
      )

    # Log the response code

    log(
      label="playlog",
      "POST status=#{response.status_code}"
    )
  end
end

# Call this with your final master source

def on_md_handler(s) =
  s.on_metadata(synchronous=false, playlog_send)
end
