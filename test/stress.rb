#!/usr/bin/ruby

require 'typhoeus'
require 'socket'
require 'time'
require 'date'

hubs=%w(localhost)
port=9129
prefix='repo'
files=%w(
  extra/os/x86_64/extra.db
  core/os/x86_64/core.db
  testing/os/x86_64/testing.db
  core/os/x86_64/linux-3.19-1-x86_64.pkg.tar.xz
  community/os/x86_64/atop-2.0.2-2-x86_64.pkg.tar.xz
  extra/os/x86_64/foo-bar.pkg.tar.xz
)

def rand(arr)
  arr[Random.rand(arr.length)]
end


hydra = Typhoeus::Hydra.new(max_concurrency: 3)

#hubs.each { |h|
  #Typhoeus::Request.get "http://#{h}:#{port}/rpc/register?pkg_path=repo/pkg&pkg_port=80&db_path=repo/db&db_port=80"
#}

for i in 1..3000 do
  hub = rand(hubs)
  file = rand(files)

  url = "http://#{hub}:#{port}/#{prefix}/#{file}"

  request = Typhoeus::Request.new(url, :method => :head, :headers => {"If-Modified-Since": Date.today.httpdate}) # followlocation: true ?
  request.on_complete do |response|
    next if [304, 307].include? response.code

    error = if response.timed_out?
      # aw hell no
      "time out"
    elsif response.code == 0
      # Could not get an http response, something's wrong.
      response.return_message
    else
      # Received a non-successful http response.
      response.code.to_s
    end

    puts "Url #{response.request.url} got error: #{error}"
  end

  hydra.queue(request)
end

hydra.run
