docker run -d --restart=always -p 8080:8080 -e="LOG=timtam://192.168.2.1:7349" -e="NODE_ENV=production" -e="REGISTER=http://192.168.2.1:2379/backend" -e="PASSWORD=mypassword" varnish-agent