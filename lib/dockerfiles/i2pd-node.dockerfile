# alpine >= 3.20 ships i2pd >= 2.51, where RouterInfo address parsing honors
# reservedrange=false (2.49 hardcodes the reserved-range rejection, which
# discards every RouterInfo on a private-subnet testnet)
FROM alpine:3.22

RUN apk add --no-cache i2pd
RUN apk add --no-cache rsync
EXPOSE 7070

CMD ["i2pd", "--conf=/var/lib/i2pd/i2pd.conf", "--datadir=/var/lib/i2pd"]
