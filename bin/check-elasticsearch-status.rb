#! /usr/bin/env ruby
# frozen_string_literal: true

require 'json'
require 'net/http'
require 'sensu-plugin/check/cli'

class CheckElasticsearchStatus < Sensu::Plugin::Check::CLI
  option :host,
         description: 'Elasticsearch host',
         short: '-h HOST',
         long: '--host HOST',
         default: '127.0.0.1'

  option :port,
         description: 'Elasticsearch port',
         short: '-p PORT',
         long: '--port PORT',
         proc: proc(&:to_i),
         default: 9200

  def run
    response = Net::HTTP.start(config[:host], config[:port]) do |connection|
      request = Net::HTTP::Get.new('/_cluster/health')
      connection.request(request)
    end

    health = JSON.parse(response.body)
    acquire_status = health['status'].downcase

    case acquire_status
    when 'green'
      ok 'Cluster state is Green'
    when 'yellow'
      warning 'Cluster state is Yellow'
    when 'red'
      critical 'Cluster state is Red'
    end
  rescue Errno::ECONNREFUSED
    critical 'Connection refused'
  rescue Errno::ECONNRESET
    critical 'Connection reset by peer'
  end
end
