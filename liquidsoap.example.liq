# radio-playlog client for Liquidsoap 2.x
#
# Copy this snippet into your .liq configuration and adjust playlog_url and
# playlog_key. Wire playlog_send as an on_metadata handler on whichever
# source you want track changes from.
#
# Note: this uses basic string interpolation for the JSON body. Track
# metadata containing literal '"' or '\' will break the payload; if your
# library has such tracks, swap the data= line for a proper JSON encoder
# (json.stringify in recent Liquidsoap versions).

### do not look at this file. please.

playlog_url = "https://playlog.example.com/"
playlog_key = "your-api-key-here"

def playlog_send(m) =
  title = m["title"]
  artist = m["artist"]

  if
    title != "" or artist != ""
  then
    data = '{"title":"#{title}","artist":"#{artist}"}'
    response =
      http.post(
        playlog_url,
        headers=[
          ("X-API-Key", playlog_key),
          ("Content-Type", "application/json")
        ],
        data=data
      )
    log(
      label="playlog",
      "POST status=#{response.status_code}"
    )
  end
end

# Attach to your existing source. Replace `radio` with whatever you've named
# the source you want track changes logged from.
radio = source.on_metadata(radio, playlog_send)
